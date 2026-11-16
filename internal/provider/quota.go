package provider

import (
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/djalmajr/soho/internal/text"
)

const jsWhitespace = `[\x{0009}-\x{000d}\x{0020}\x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}-\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

var (
	quotaCodeStartRE    = regexp.MustCompile(`^` + jsWhitespace + `*(#|//|/\*)`)
	quotaCommentHashRE  = regexp.MustCompile(jsWhitespace + `#(` + jsWhitespace + `|$)`)
	quotaCommentRE      = regexp.MustCompile(`(^|[^:])//|/\*`)
	quotaReturnRE       = regexp.MustCompile(`(^|[^A-Za-z0-9_])return([^A-Za-z0-9_]|$)`)
	quotaFunctionRE     = regexp.MustCompile(`(^|[^A-Za-z0-9_])function([^A-Za-z0-9_]|$)`)
	quotaFuncRE         = regexp.MustCompile(`(^|[^A-Za-z0-9_])func` + jsWhitespace)
	quotaAssignRE       = regexp.MustCompile(`[A-Za-z0-9_]` + jsWhitespace + `+` + `=` + jsWhitespace + `*`)
	quotaQuotedAssignRE = regexp.MustCompile(`[A-Za-z0-9_]=["']`)
	diskFullRE          = regexp.MustCompile(`disk quota exceeded|no space left on device|edquot|enospc`)
	quotaRes            = []*regexp.Regexp{
		regexp.MustCompile(`hit your usage limit`),
		regexp.MustCompile(`individual quota reached`),
		regexp.MustCompile(`you exceeded your current quota`),
		regexp.MustCompile(`quota exceeded`),
		regexp.MustCompile(`resource_exhausted`),
		regexp.MustCompile(`429 too many requests`),
		regexp.MustCompile(`rate limit exceeded`),
		regexp.MustCompile(`you've hit your( [a-z]+)? limit`),
		regexp.MustCompile(`you have hit your( [a-z]+)? limit`),
		regexp.MustCompile(`you have reached your( specified)?( (workspace )?api)? usage limits?`),
		regexp.MustCompile(`you've reached your( specified)?( (workspace )?api)? usage limits?`),
		regexp.MustCompile(`\b402\b.*\b(insufficient|payment required|funds)\b|\b(insufficient|payment required|funds)\b.*\b402\b`),
		regexp.MustCompile(`insufficient (account )?(funds|balance|credits)`),
	}
	renewalLineRes = []*regexp.Regexp{
		regexp.MustCompile(`resets? (at|in|on) `),
		regexp.MustCompile(`try again (at|in) `),
		regexp.MustCompile(`available (again )?(at|in) `),
		regexp.MustCompile(`retry after `),
		regexp.MustCompile(`in [0-9]+ (minute|hour|second)s?`),
	}
	renewalValueRes = []*regexp.Regexp{
		regexp.MustCompile(`[0-9]{1,2}:[0-9]{2}(:[0-9]{2})?([ap]\.m\.)?`),
		regexp.MustCompile(`[0-9]+ (minute|hour|second|day)s?`),
	}
	renewalDateRE = regexp.MustCompile(`[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}`)
)

// QuotaLineIsCode detects source lines so matching words in code do not count as provider errors.
func QuotaLineIsCode(line string) bool {
	return quotaCodeStartRE.MatchString(line) || quotaCommentHashRE.MatchString(line) || quotaCommentRE.MatchString(line) ||
		quotaReturnRE.MatchString(line) || quotaFunctionRE.MatchString(line) || quotaFuncRE.MatchString(line) ||
		quotaAssignRE.MatchString(line) || quotaQuotedAssignRE.MatchString(line)
}

// RenewalValue returns the leftmost recognized time, date, or duration in line.
func RenewalValue(line string) string {
	lowerLine := text.ASCIILower(line)
	bestStart := -1
	bestText := ""
	for _, re := range renewalValueRes {
		loc := re.FindStringIndex(lowerLine)
		if loc != nil && (bestStart == -1 || loc[0] < bestStart) {
			bestStart = loc[0]
			bestText = line[loc[0]:loc[1]]
		}
	}
	if loc := renewalDateRE.FindStringIndex(line); loc != nil && (bestStart == -1 || loc[0] < bestStart) {
		bestText = line[loc[0]:loc[1]]
	}
	return bestText
}

// QuotaPhraseQuoted reports whether the first regex match is a quoted literal rather than provider prose.
func QuotaPhraseQuoted(line string, re *regexp.Regexp) bool {
	lowerLine := strings.ReplaceAll(text.ASCIILower(line), "K", "k")
	match := re.FindStringIndex(lowerLine)
	if match == nil {
		return false
	}
	prefix := lowerLine[:match[0]]
	start := len(utf16.Encode([]rune(prefix))) + strings.Count(prefix, "İ")
	matchLength := len(utf16.Encode([]rune(lowerLine[match[0]:match[1]])))
	pre := strings.TrimRight(sliceUTF16(line, 0, start), " \t")
	post := strings.TrimLeft(sliceUTF16(line, start+matchLength, utf16Length(line)), " \t")
	if strings.Contains(pre, `"message"`) || strings.Contains(pre, "insufficient_quota") || strings.Contains(pre, "rate_limit_error") {
		return false
	}
	var previous, next byte
	if len(pre) > 0 {
		previous = pre[len(pre)-1]
	}
	if len(post) > 0 {
		next = post[0]
	}
	return (previous == '"' && next == '"') || (previous == '\'' && next == '\'') || (previous == '`' && next == '`')
}

func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func sliceUTF16(value string, start, end int) string {
	units := utf16.Encode([]rune(value))
	if start > len(units) {
		start = len(units)
	}
	if end > len(units) {
		end = len(units)
	}
	if end < start {
		end = start
	}
	return string(utf16.Decode(units[start:end]))
}

// DiskFullDetect returns the first sanitized disk-space error line, excluding source and quoted literals.
func DiskFullDetect(state, screen string) string {
	if state == "working" || screen == "" {
		return ""
	}
	for _, line := range strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n") {
		if diskFullRE.MatchString(text.ASCIILower(line)) && !QuotaLineIsCode(line) && !QuotaPhraseQuoted(line, diskFullRE) {
			return text.SanitizeCause(text.RedactSecrets(line))
		}
	}
	return ""
}

// QuotaDetect returns the sanitized quota and renewal lines, or nil when the screen is not a quota stop.
func QuotaDetect(state, screen string) []string {
	if state == "working" || screen == "" {
		return nil
	}
	textScreen := strings.ReplaceAll(screen, "\r\n", "\n")
	lines := strings.Split(textScreen, "\n")
	matched := ""
	for _, candidate := range lines {
		if candidate == "" || diskFullRE.MatchString(text.ASCIILower(candidate)) || QuotaLineIsCode(candidate) {
			continue
		}
		for _, re := range quotaRes {
			if !re.MatchString(text.ASCIILower(candidate)) || QuotaPhraseQuoted(candidate, re) {
				continue
			}
			matched = candidate
			break
		}
		if matched != "" {
			break
		}
	}
	if matched == "" {
		return nil
	}
	renewal := ""
	for _, line := range lines {
		lowerLine := text.ASCIILower(line)
		for _, re := range renewalLineRes {
			if re.MatchString(lowerLine) {
				renewal = line
				break
			}
		}
		if renewal != "" {
			break
		}
	}
	return []string{text.SanitizeCause(text.RedactSecrets(matched)), text.SanitizeCause(text.RedactSecrets(renewal))}
}
