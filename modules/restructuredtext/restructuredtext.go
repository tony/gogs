// Copyright 2014 The Gogs Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package restructuredtext

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Unknwon/com"
	"github.com/hhatto/gorst"
	"golang.org/x/net/html"

	"github.com/gogits/gogs/modules/base"
	"github.com/gogits/gogs/modules/markdown"
	"github.com/gogits/gogs/modules/setting"
)

var validLinksPattern = regexp.MustCompile(`^[a-z][\w-]+://`)

// isLink reports whether link fits valid format.
func isLink(link []byte) bool {
	return validLinksPattern.Match(link)
}

// IsReStructuredTextFile reports whether name looks like a reST file
// based on its extension.
func IsReStructuredTextFile(name string) bool {
	name = strings.ToLower(name)
	switch filepath.Ext(name) {
	case ".rst":
		return true
	}
	return false
}

var (
	// MentionPattern matches string that mentions someone, e.g. @Unknwon
	MentionPattern = regexp.MustCompile(`(\s|^)@[0-9a-zA-Z_\.]+`)

	// CommitPattern matches link to certain commit with or without trailing hash,
	// e.g. https://try.gogs.io/gogs/gogs/commit/d8a994ef243349f321568f9e36d5c3f444b99cae#diff-2
	CommitPattern = regexp.MustCompile(`(\s|^)https?.*commit/[0-9a-zA-Z]+(#+[0-9a-zA-Z-]*)?`)

	// IssueFullPattern matches link to an issue with or without trailing hash,
	// e.g. https://try.gogs.io/gogs/gogs/issues/4#issue-685
	IssueFullPattern = regexp.MustCompile(`(\s|^)https?.*issues/[0-9]+(#+[0-9a-zA-Z-]*)?`)
	// IssueIndexPattern matches string that references to an issue, e.g. #1287
	IssueIndexPattern = regexp.MustCompile(`( |^|\()#[0-9]+\b`)

	// Sha1CurrentPattern matches string that represents a commit SHA, e.g. d8a994ef243349f321568f9e36d5c3f444b99cae
	Sha1CurrentPattern = regexp.MustCompile(`\b[0-9a-f]{40}\b`)
)

// Note: this section is for purpose of increase performance and
// reduce memory allocation at runtime since they are constant literals.
var (
	svgSuffix         = []byte(".svg")
	svgSuffixWithMark = []byte(".svg?")
	spaceBytes        = []byte(" ")
	spaceEncodedBytes = []byte("%20")
	space             = " "
	spaceEncoded      = "%20"
)

// cutoutVerbosePrefix cutouts URL prefix including sub-path to
// return a clean unified string of request URL path.
func cutoutVerbosePrefix(prefix string) string {
	count := 0
	for i := 0; i < len(prefix); i++ {
		if prefix[i] == '/' {
			count++
		}
		if count >= 3+setting.AppSubUrlDepth {
			return prefix[:i]
		}
	}
	return prefix
}

// RenderIssueIndexPattern renders issue indexes to corresponding links.
func RenderIssueIndexPattern(rawBytes []byte, urlPrefix string, metas map[string]string) []byte {
	urlPrefix = cutoutVerbosePrefix(urlPrefix)
	ms := IssueIndexPattern.FindAll(rawBytes, -1)
	for _, m := range ms {
		var space string
		if m[0] != '#' {
			space = string(m[0])
			m = m[1:]
		}
		if metas == nil {
			rawBytes = bytes.Replace(rawBytes, m, []byte(fmt.Sprintf(`%s<a href="%s/issues/%s">%s</a>`,
				space, urlPrefix, m[1:], m)), 1)
		} else {
			// Support for external issue tracker
			metas["index"] = string(m[1:])
			rawBytes = bytes.Replace(rawBytes, m, []byte(fmt.Sprintf(`%s<a href="%s">%s</a>`,
				space, com.Expand(metas["format"], metas), m)), 1)
		}
	}
	return rawBytes
}

// RenderSha1CurrentPattern renders SHA1 strings to corresponding links that assumes in the same repository.
func RenderSha1CurrentPattern(rawBytes []byte, urlPrefix string) []byte {
	ms := Sha1CurrentPattern.FindAll(rawBytes, -1)
	for _, m := range ms {
		rawBytes = bytes.Replace(rawBytes, m, []byte(fmt.Sprintf(
			`<a href="%s/commit/%s"><code>%s</code></a>`, urlPrefix, m, base.ShortSha(string(m)))), -1)
	}
	return rawBytes
}

// RenderSpecialLink renders mentions, indexes and SHA1 strings to corresponding links.
func RenderSpecialLink(rawBytes []byte, urlPrefix string, metas map[string]string) []byte {
	ms := MentionPattern.FindAll(rawBytes, -1)
	for _, m := range ms {
		m = bytes.TrimSpace(m)
		rawBytes = bytes.Replace(rawBytes, m,
			[]byte(fmt.Sprintf(`<a href="%s/%s">%s</a>`, setting.AppSubUrl, m[1:], m)), -1)
	}

	rawBytes = RenderIssueIndexPattern(rawBytes, urlPrefix, metas)
	rawBytes = RenderSha1CurrentPattern(rawBytes, urlPrefix)
	return rawBytes
}

// RenderRaw renders Markdown to HTML without handling special links.
func RenderRaw(body []byte, urlPrefix string) []byte {
	p := rst.NewParser(nil)
	var buf bytes.Buffer
	html := rst.ToHTML(&buf)
	p.ReStructuredText(bytes.NewReader(body), html)
	fmt.Println(html)
	return buf.Bytes()
}

var (
	leftAngleBracket  = []byte("</")
	rightAngleBracket = []byte(">")
)

var noEndTags = []string{"img", "input", "br", "hr"}

// PostProcess treats different types of HTML differently,
// and only renders special links for plain text blocks.
func PostProcess(rawHtml []byte, urlPrefix string, metas map[string]string) []byte {
	startTags := make([]string, 0, 5)
	var buf bytes.Buffer
	tokenizer := html.NewTokenizer(bytes.NewReader(rawHtml))

OUTER_LOOP:
	for html.ErrorToken != tokenizer.Next() {
		token := tokenizer.Token()
		switch token.Type {
		case html.TextToken:
			buf.Write(RenderSpecialLink([]byte(token.String()), urlPrefix, metas))

		case html.StartTagToken:
			buf.WriteString(token.String())
			tagName := token.Data
			// If this is an excluded tag, we skip processing all output until a close tag is encountered.
			if strings.EqualFold("a", tagName) || strings.EqualFold("code", tagName) || strings.EqualFold("pre", tagName) {
				stackNum := 1
				for html.ErrorToken != tokenizer.Next() {
					token = tokenizer.Token()

					// Copy the token to the output verbatim
					buf.WriteString(token.String())

					if token.Type == html.StartTagToken {
						stackNum++
					}

					// If this is the close tag to the outer-most, we are done
					if token.Type == html.EndTagToken {
						stackNum--

						if stackNum <= 0 && strings.EqualFold(tagName, token.Data) {
							break
						}
					}
				}
				continue OUTER_LOOP
			}

			if !com.IsSliceContainsStr(noEndTags, token.Data) {
				startTags = append(startTags, token.Data)
			}

		case html.EndTagToken:
			if len(startTags) == 0 {
				buf.WriteString(token.String())
				break
			}

			buf.Write(leftAngleBracket)
			buf.WriteString(startTags[len(startTags)-1])
			buf.Write(rightAngleBracket)
			startTags = startTags[:len(startTags)-1]
		default:
			buf.WriteString(token.String())
		}
	}

	if io.EOF == tokenizer.Err() {
		return buf.Bytes()
	}

	// If we are not at the end of the input, then some other parsing error has occurred,
	// so return the input verbatim.
	return rawHtml
}

// Render renders Markdown to HTML with special links.
func Render(rawBytes []byte, urlPrefix string, metas map[string]string) []byte {
	urlPrefix = strings.Replace(urlPrefix, space, spaceEncoded, -1)
	result := RenderRaw(rawBytes, urlPrefix)
	result = PostProcess(result, urlPrefix, metas)
	result = markdown.Sanitizer.SanitizeBytes(result)
	return result
}

// RenderString renders Markdown to HTML with special links and returns string type.
func RenderString(raw, urlPrefix string, metas map[string]string) string {
	return string(Render([]byte(raw), urlPrefix, metas))
}
