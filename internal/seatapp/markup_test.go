package seatapp

import (
	"encoding/xml"
	"errors"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// The app emits explicit end tags apart from HTML's void elements. RawToken
// lets us inspect those elements and decoded attributes using the standard
// library, including partial fragments in the inventory's negative controls.
type pageElement struct {
	name  string
	attrs map[string]string
}

func pageElements(page string) ([]pageElement, error) {
	decoder := xml.NewDecoder(strings.NewReader(page))
	decoder.Strict = false
	decoder.Entity = map[string]string{}
	for _, word := range namedEntity.FindAllString(page, -1) {
		decoder.Entity[word[1:len(word)-1]] = html.UnescapeString(word)
	}
	var elements []pageElement
	inBody := false
	for {
		tok, err := decoder.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			if inBody {
				return nil, errors.New("element inside chat body")
			}
			el := pageElement{strings.ToLower(e.Name.Local), map[string]string{}}
			for _, a := range e.Attr {
				name := strings.ToLower(a.Name.Local)
				if _, ok := el.attrs[name]; ok {
					return nil, errors.New("duplicate attribute")
				}
				el.attrs[name] = a.Value
			}
			elements = append(elements, el)
			inBody = el.name == "p" && strings.Contains(" "+el.attrs["class"]+" ", " chat-body ")
		case xml.EndElement:
			if inBody && e.Name.Local != "p" {
				return nil, errors.New("unexpected end element in chat body")
			}
			inBody = false
		case xml.CharData:
		default:
			if inBody {
				return nil, errors.New("non-text content in chat body")
			}
		}
	}
	if inBody {
		return nil, errors.New("unclosed chat body")
	}
	return elements, nil
}

var namedEntity = regexp.MustCompile(`&[A-Za-z][A-Za-z0-9]*;`)
var chatTarget = regexp.MustCompile(`^/leads/daemon/[a-z0-9][a-z0-9-]{0,31}(?:\?after=(0|[1-9][0-9]*))?(?:#newest)?$`)

func chatHref(target string) bool {
	if target == "/leads/" {
		return true
	}
	m := chatTarget.FindStringSubmatch(target)
	if m == nil {
		return false
	}
	if m[1] == "" {
		return true
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	return err == nil && chatNumber(n)
}

func listedChatLink(id, href string) bool {
	switch id {
	case "skip":
		return href == "#content"
	case "nav-cards":
		return href == "/cards/"
	case "nav-projects":
		return href == "/projects/"
	case "nav-leads":
		return href == "/leads/"
	case "chat-refresh":
		return chatHref(href) && !strings.ContainsAny(href, "?#")
	case "chat-jump":
		return href == "#newest" || (chatHref(href) && strings.Contains(href, "?after=") && strings.HasSuffix(href, "#newest"))
	case "chat-prev", "chat-next":
		return chatHref(href) && strings.Contains(href, "?after=") && !strings.Contains(href, "#")
	case "":
		return chatHref(href) && strings.HasSuffix(href, "#newest") && !strings.Contains(href, "?")
	}
	return false
}
