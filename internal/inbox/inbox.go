// Package inbox interprets the user-maintained Markdown without modifying it.
package inbox

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"

	"github.com/CesarBraz7/noema/internal/adapters/video"
	"golang.org/x/net/idna"
)

var (
	listPrefix = regexp.MustCompile(`^(?:[-*+]|[0-9]+[.)])[ \t]+`)
	checkBox   = regexp.MustCompile(`^\[[ xX]\][ \t]+`)
	link       = regexp.MustCompile(`^\[[^\]]+\]\((https?://\S+?)\)(?:\s|$)`)
	comment    = regexp.MustCompile(`(?s)<!--.*?(?:-->|$)`)
)

type Entry struct {
	URL   string
	Focus string
	Video bool
}

// Parse accepts bare URLs, Markdown links and lists, including checked items.
func Parse(markdown string) []Entry {
	markdown = strings.ReplaceAll(strings.ToValidUTF8(markdown, "�"), "\x00", "�")
	var entries []Entry
	current := -1
	var focus strings.Builder
	finish := func() {
		if current >= 0 {
			entries[current].Focus = focus.String()
			current = -1
		}
		focus.Reset()
	}
	var fence string
	for _, line := range strings.Split(comment.ReplaceAllStringFunc(markdown, func(s string) string {
		return strings.Repeat("\n", strings.Count(s, "\n"))
	}), "\n") {
		trimmed := strings.TrimSpace(line)
		marker := strings.TrimLeft(trimmed, "`~")
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) && marker == "" {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:len(trimmed)-len(marker)]
			finish()
			continue
		}
		item := checkBox.ReplaceAllString(listPrefix.ReplaceAllString(trimmed, ""), "")
		if target := itemURL(item); target != "" {
			finish()
			canonical, err := CanonicalURL(target)
			if err != nil {
				continue
			}
			entry := Entry{URL: canonical}
			if !isArticle(canonical) {
				entry.URL, err = video.CanonicalURL(canonical)
				if err != nil {
					continue
				}
				entry.Video = true
			}
			entries = append(entries, entry)
			current = len(entries) - 1
			continue
		}
		if trimmed == "" {
			continue
		}
		if current >= 0 && (line[0] == ' ' || line[0] == '\t') {
			if focus.Len() > 0 {
				focus.WriteByte('\n')
			}
			focus.WriteString(item)
			continue
		}
		finish()
	}
	finish()
	return entries
}

func itemURL(item string) string {
	if match := link.FindStringSubmatch(item); match != nil {
		return match[1]
	}
	fields := strings.Fields(item)
	if len(fields) == 0 {
		return ""
	}
	candidate := strings.TrimPrefix(strings.TrimSuffix(fields[0], ">"), "<")
	if strings.HasPrefix(strings.ToLower(candidate), "https://") || strings.HasPrefix(strings.ToLower(candidate), "http://") {
		return candidate
	}
	return ""
}

// CanonicalURL preserves application query parameters, their order and escaping.
func CanonicalURL(raw string) (string, error) {
	if len(raw) > 4096 || strings.ContainsAny(raw, "<>\"\\ \t\r\n") {
		return "", errors.New("URL inválida")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "" {
		return "", errors.New("URL HTTP(S) inválida")
	}
	if port := u.Port(); port != "" && (u.Scheme == "https" && port != "443" || u.Scheme == "http" && port != "80") {
		return "", errors.New("porta HTTP(S) não permitida")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if addr, err := netip.ParseAddr(host); err == nil && addr.Zone() == "" {
		host = addr.String()
		if addr.Is6() {
			host = "[" + host + "]"
		}
	} else {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
			return "", errors.New("host inválido")
		}
	}
	u.Host = host
	if u.Path == "" {
		u.Path = "/"
	}
	u.Fragment, u.RawFragment = "", ""
	var query []string
	for _, field := range strings.Split(u.RawQuery, "&") {
		key, _, _ := strings.Cut(field, "=")
		key, err = url.QueryUnescape(key)
		if err != nil {
			return "", errors.New("query inválida")
		}
		switch key = strings.ToLower(key); key {
		case "fbclid", "gclid", "dclid", "msclkid":
			continue
		}
		if !strings.HasPrefix(key, "utm_") {
			query = append(query, field)
		}
	}
	u.RawQuery = strings.Join(query, "&")
	u.ForceQuery = false
	return u.String(), nil
}

func isArticle(canonical string) bool {
	u, _ := url.Parse(canonical)
	for _, platform := range []string{"youtube.com", "youtu.be", "instagram.com", "x.com", "twitter.com"} {
		if host := u.Hostname(); host == platform || strings.HasSuffix(host, "."+platform) {
			return false
		}
	}
	return true
}
