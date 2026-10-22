// Package ldaptest is a minimal LDAPS server for tests: simple bind, search
// with and/or/not/equality/present filters, over TLS with the test PKI. It
// behaves like OpenLDAP where it matters for estopo (an empty password is an
// unauthenticated bind that succeeds; searches need an authenticated bind).
// The real OpenLDAP is exercised by testenv/ldap.
package ldaptest

import (
	"crypto/tls"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	ber "github.com/go-asn1-ber/asn1-ber"

	"estopo/internal/testpki"
)

// Directory content.
const (
	Base           = "dc=example,dc=org"
	ServiceDN      = "cn=estopo,ou=services," + Base
	ServicePass    = "service-secret"
	PeopleBase     = "ou=people," + Base
	GroupBase      = "ou=groups," + Base
	AdminGroupDN   = "cn=estopo-admins," + GroupBase
	ReaderGroupDN  = "cn=estopo-readers," + GroupBase
	PosixGroupDN   = "cn=ops," + GroupBase
	UnusedGroupDN  = "cn=others," + GroupBase
	AlicePassword  = "alice-password" // alice: admin (groupOfNames)
	BobPassword    = "bob-password"   // bob: reader (groupOfUniqueNames)
	CarolPassword  = "carol-password" // carol: no estopo group
	DavePassword   = "dave-password"  // dave: reader (posixGroup memberUid)
	ServiceAccount = "estopo"
)

// Entry is a directory entry.
type Entry struct {
	DN       string
	Attrs    map[string][]string // lower-case attribute names
	Password string
}

// Server is a running test directory.
type Server struct {
	URL              string // ldaps://127.0.0.1:<port>
	CAFile           string
	BindPasswordFile string // contains ServicePass

	ln      net.Listener
	entries []Entry

	mu    sync.Mutex
	binds []string // "dn:ok|fail|anonymous"
}

// Start runs a directory with alice, bob, carol, dave and their groups.
func Start(tb testing.TB) *Server {
	tb.Helper()
	dir := tb.TempDir()
	pki, err := testpki.Generate(dir)
	if err != nil {
		tb.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(pki.ServerCert, pki.ServerKey)
	if err != nil {
		tb.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		tb.Fatal(err)
	}
	pw := filepath.Join(dir, "bind.pw")
	if err := os.WriteFile(pw, []byte(ServicePass+"\n"), 0o600); err != nil {
		tb.Fatal(err)
	}
	s := &Server{URL: "ldaps://" + ln.Addr().String(), CAFile: pki.RootCert, BindPasswordFile: pw, ln: ln, entries: directory()}
	go s.serve()
	tb.Cleanup(func() { ln.Close() })
	return s
}

func person(uid, pw string) Entry {
	return Entry{DN: "uid=" + uid + "," + PeopleBase, Password: pw,
		Attrs: map[string][]string{"objectclass": {"inetOrgPerson"}, "uid": {uid}, "cn": {uid}, "mail": {uid + "@example.org"}}}
}

func directory() []Entry {
	return []Entry{
		{DN: ServiceDN, Password: ServicePass, Attrs: map[string][]string{"objectclass": {"organizationalRole"}, "cn": {ServiceAccount}}},
		person("alice", AlicePassword),
		person("bob", BobPassword),
		person("carol", CarolPassword),
		person("dave", DavePassword),
		{DN: AdminGroupDN, Attrs: map[string][]string{"objectclass": {"groupOfNames"}, "member": {"uid=alice," + PeopleBase}}},
		// Spacing and case differ on purpose: DNs compare like LDAP does.
		{DN: ReaderGroupDN, Attrs: map[string][]string{"objectclass": {"groupOfUniqueNames"}, "uniquemember": {"UID=bob, ou=People, dc=example, dc=org"}}},
		{DN: PosixGroupDN, Attrs: map[string][]string{"objectclass": {"posixGroup"}, "memberuid": {"dave"}}},
		{DN: UnusedGroupDN, Attrs: map[string][]string{"objectclass": {"groupOfNames"}, "member": {"uid=carol," + PeopleBase}}},
	}
}

// Binds returns the binds received so far ("dn:ok", "dn:fail", "dn:anonymous").
func (s *Server) Binds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.binds...)
}

func (s *Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

// LDAP result codes and application tags (RFC 4511).
const (
	codeSuccess           = 0
	codeSizeLimitExceeded = 4
	codeInvalidCreds      = 49
	codeInsufficientRight = 50
	codeProtocolError     = 2

	tagBindRequest    = 0
	tagBindResponse   = 1
	tagUnbindRequest  = 2
	tagSearchRequest  = 3
	tagSearchEntry    = 4
	tagSearchDone     = 5
	tagExtendedResult = 24
)

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	bound := "" // authenticated DN
	for {
		p, err := ber.ReadPacket(c)
		if err != nil {
			return
		}
		if len(p.Children) < 2 {
			return
		}
		id, _ := p.Children[0].Value.(int64)
		op := p.Children[1]
		switch op.Tag {
		case tagBindRequest:
			code := s.bind(op, &bound)
			c.Write(envelope(id, result(tagBindResponse, code)).Bytes())
		case tagSearchRequest:
			if bound == "" {
				c.Write(envelope(id, result(tagSearchDone, codeInsufficientRight)).Bytes())
				continue
			}
			s.search(c, id, op)
		case tagUnbindRequest:
			return
		default:
			c.Write(envelope(id, result(tagExtendedResult, codeProtocolError)).Bytes())
		}
	}
}

func (s *Server) bind(op *ber.Packet, bound *string) int {
	if len(op.Children) < 3 {
		return codeProtocolError
	}
	dn, pw := str(op.Children[1]), str(op.Children[2])
	s.mu.Lock()
	defer s.mu.Unlock()
	if pw == "" {
		// RFC 4513 §5.1.2: unauthenticated bind, accepted by OpenLDAP.
		s.binds = append(s.binds, dn+":anonymous")
		*bound = ""
		return codeSuccess
	}
	for _, e := range s.entries {
		if normDN(e.DN) == normDN(dn) && e.Password != "" && e.Password == pw {
			s.binds = append(s.binds, dn+":ok")
			*bound = e.DN
			return codeSuccess
		}
	}
	s.binds = append(s.binds, dn+":fail")
	*bound = ""
	return codeInvalidCreds
}

func (s *Server) search(w io.Writer, id int64, op *ber.Packet) {
	if len(op.Children) < 8 {
		w.Write(envelope(id, result(tagSearchDone, codeProtocolError)).Bytes())
		return
	}
	base := normDN(str(op.Children[0]))
	sizeLimit, _ := op.Children[3].Value.(int64)
	filter := op.Children[6]
	n := 0
	for _, e := range s.entries {
		dn := normDN(e.DN)
		if dn != base && !strings.HasSuffix(dn, ","+base) {
			continue
		}
		if !match(filter, e) {
			continue
		}
		if sizeLimit > 0 && int64(n) >= sizeLimit {
			w.Write(envelope(id, result(tagSearchDone, codeSizeLimitExceeded)).Bytes())
			return
		}
		entry := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tagSearchEntry, nil, "")
		entry.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, e.DN, ""))
		attrs := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "")
		for _, a := range op.Children[7].Children {
			name := strings.ToLower(str(a))
			vals, ok := e.Attrs[name]
			if !ok {
				continue
			}
			pa := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "")
			pa.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, name, ""))
			set := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSet, nil, "")
			for _, v := range vals {
				set.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, v, ""))
			}
			pa.AppendChild(set)
			attrs.AppendChild(pa)
		}
		entry.AppendChild(attrs)
		w.Write(envelope(id, entry).Bytes())
		n++
	}
	w.Write(envelope(id, result(tagSearchDone, codeSuccess)).Bytes())
}

// match evaluates and (0), or (1), not (2), equalityMatch (3) and present (7).
func match(f *ber.Packet, e Entry) bool {
	switch f.Tag {
	case 0:
		for _, c := range f.Children {
			if !match(c, e) {
				return false
			}
		}
		return true
	case 1:
		for _, c := range f.Children {
			if match(c, e) {
				return true
			}
		}
		return false
	case 2:
		return len(f.Children) == 1 && !match(f.Children[0], e)
	case 3:
		if len(f.Children) != 2 {
			return false
		}
		attr, want := strings.ToLower(str(f.Children[0])), str(f.Children[1])
		for _, v := range e.Attrs[attr] {
			if strings.EqualFold(v, want) || normDN(v) == normDN(want) {
				return true
			}
		}
		return false
	case 7:
		return len(e.Attrs[strings.ToLower(str(f))]) > 0
	}
	return false
}

// normDN lower-cases a DN and removes the spaces around separators.
func normDN(dn string) string {
	parts := strings.Split(dn, ",")
	for i, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		for j := range kv {
			kv[j] = strings.TrimSpace(kv[j])
		}
		parts[i] = strings.ToLower(strings.Join(kv, "="))
	}
	return strings.Join(parts, ",")
}

func str(p *ber.Packet) string {
	if p.Data == nil {
		return ""
	}
	return p.Data.String()
}

func envelope(id int64, op *ber.Packet) *ber.Packet {
	p := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "")
	p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, id, ""))
	p.AppendChild(op)
	return p
}

func result(tag ber.Tag, code int) *ber.Packet {
	op := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tag, nil, "")
	op.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, code, ""))
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "", ""))
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "", ""))
	return op
}
