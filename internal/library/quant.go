package library

import (
	"regexp"
	"strings"
)

var (
	quantRe = regexp.MustCompile(`(?i)(?:^|[-_.])((?:UD-)?(?:IQ\d(?:_[A-Z]+)*|Q\d(?:_[0-9A-Z]+)*|MXFP4(?:_MOE)?|BF16|F16|F32))(?:[-_.]|$)`)
	shardRe = regexp.MustCompile(`(?i)-(\d{5})-of-(\d{5})\.gguf$`)
)

// quantFromName extracts a quantisation label such as Q4_K_M or MXFP4 from a file name.
func quantFromName(name string) string {
	if m := quantRe.FindStringSubmatch(name); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

// isMMProjName reports whether a file name follows llama.cpp's mmproj naming.
func isMMProjName(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "mmproj")
}

// shardInfo returns the shard index and total of a "-00001-of-00004.gguf" file name.
func shardInfo(name string) (idx, total int, ok bool) {
	m := shardRe.FindStringSubmatch(name)
	if m == nil {
		return 0, 0, false
	}
	for _, c := range m[1] {
		idx = idx*10 + int(c-'0')
	}
	for _, c := range m[2] {
		total = total*10 + int(c-'0')
	}
	return idx, total, true
}
