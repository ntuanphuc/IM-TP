package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

type Directory struct {
	URLs                                                             []string
	Base, BindDN, BindPassword, TeacherGroup, StaffGroup, AdminGroup string
	tls                                                              *tls.Config
}

type Account struct {
	GUID, Username, Name            string
	Disabled, Teacher, Staff, Admin bool
}

func DirectoryFromEnv() (Directory, error) {
	d := Directory{Base: os.Getenv("AD_BASE_DN"), BindDN: os.Getenv("AD_BIND_DN"), BindPassword: os.Getenv("AD_BIND_PASSWORD"), TeacherGroup: os.Getenv("AD_TEACHER_GROUP_DN"), StaffGroup: os.Getenv("AD_STAFF_GROUP_DN"), AdminGroup: os.Getenv("AD_ADMIN_GROUP_DN")}
	d.tls = &tls.Config{MinVersion: tls.VersionTLS12}
	if encoded := os.Getenv("AD_CA_PEM_BASE64"); encoded != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			return d, err
		}
		pem, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return d, err
		}
		if !roots.AppendCertsFromPEM(pem) {
			return d, errors.New("invalid AD CA file")
		}
		d.tls.RootCAs = roots
	}
	for _, url := range strings.Split(os.Getenv("AD_URLS"), ",") {
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}
		if !strings.HasPrefix(url, "ldaps://") {
			return d, errors.New("AD URLs must use ldaps://")
		}
		d.URLs = append(d.URLs, url)
	}
	if len(d.URLs) > 0 && (d.Base == "" || d.BindDN == "" || d.BindPassword == "") {
		return d, errors.New("incomplete AD configuration")
	}
	return d, nil
}

var ErrCredentials = errors.New("invalid credentials")
var attrs = []string{"objectGUID", "sAMAccountName", "displayName", "userAccountControl", "memberOf"}

func (d Directory) account(entry *ldap.Entry) (Account, error) {
	guid := entry.GetRawAttributeValue("objectGUID")
	flags, err := strconv.Atoi(entry.GetAttributeValue("userAccountControl"))
	username := strings.ToLower(entry.GetAttributeValue("sAMAccountName"))
	if len(guid) != 16 || err != nil || username == "" {
		return Account{}, errors.New("AD entry missing required attributes")
	}
	name := entry.GetAttributeValue("displayName")
	if name == "" {
		name = username
	}
	result := Account{GUID: fmt.Sprintf("%x", guid), Username: username, Name: name, Disabled: flags&2 != 0}
	for _, group := range entry.GetAttributeValues("memberOf") {
		result.Teacher = result.Teacher || (d.TeacherGroup != "" && strings.EqualFold(group, d.TeacherGroup))
		result.Staff = result.Staff || (d.StaffGroup != "" && strings.EqualFold(group, d.StaffGroup))
		result.Admin = result.Admin || (d.AdminGroup != "" && strings.EqualFold(group, d.AdminGroup))
	}
	return result, nil
}

func (d Directory) search(ctx context.Context, filter, password string, login bool) ([]Account, error) {
	var last = errors.New("AD is not configured")
	for _, url := range d.URLs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		values, err := d.searchDC(url, filter, password, login)
		if err == nil || errors.Is(err, ErrCredentials) {
			return values, err
		}
		last = err
	}
	return nil, last
}

func (d Directory) searchDC(url, filter, password string, login bool) ([]Account, error) {
	conn, err := ldap.DialURL(url, ldap.DialWithTLSConfig(d.tls.Clone()), ldap.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetTimeout(5 * time.Second)
	if err = conn.Bind(d.BindDN, d.BindPassword); err != nil {
		return nil, err
	}
	req := ldap.NewSearchRequest(d.Base, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 5, false, filter, attrs, nil)
	var result *ldap.SearchResult
	if login {
		result, err = conn.Search(req)
	} else {
		result, err = conn.SearchWithPaging(req, 500)
	}
	if err != nil {
		return nil, err
	}
	if len(result.Referrals) > 0 {
		return nil, errors.New("AD returned unhandled referrals")
	}
	if login && len(result.Entries) != 1 {
		return nil, ErrCredentials
	}
	accounts := make([]Account, 0, len(result.Entries))
	for _, entry := range result.Entries {
		a, err := d.account(entry)
		if err != nil {
			return nil, err
		}
		if login {
			if a.Disabled {
				return nil, ErrCredentials
			}
			if err = conn.Bind(entry.DN, password); err != nil {
				if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
					return nil, ErrCredentials
				}
				return nil, err
			}
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

func (d Directory) Verify(ctx context.Context, username, password string) (Account, error) {
	if password == "" {
		return Account{}, ErrCredentials
	}
	u := ldap.EscapeFilter(username)
	accounts, err := d.search(ctx, "(&(objectCategory=person)(objectClass=user)(|(sAMAccountName="+u+")(userPrincipalName="+u+")))", password, true)
	if err != nil {
		return Account{}, err
	}
	return accounts[0], nil
}

func (d Directory) List(ctx context.Context) ([]Account, error) {
	return d.search(ctx, "(&(objectCategory=person)(objectClass=user))", "", false)
}
