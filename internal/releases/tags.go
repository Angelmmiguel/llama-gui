package releases

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// Channel is a release stream, decided by the tag name only: GitHub's prerelease flag is
// false on old b* tags.
type Channel string

// Release channels. ChannelNone is every other tag, such as the old master-* ones.
const (
	ChannelStable  Channel = "stable"
	ChannelNightly Channel = "nightly"
	ChannelNone    Channel = ""
)

// ErrInvalidTag means a tag is not a well-formed v* or b* tag.
var ErrInvalidTag = errors.New("invalid release tag")

var (
	stableTagRE  = regexp.MustCompile(`^v\d+(?:[.-][0-9A-Za-z]+)*$`)
	nightlyTagRE = regexp.MustCompile(`^b\d+$`)
)

const maxTagLen = 64

// ClassifyTag returns the channel a tag belongs to.
func ClassifyTag(tag string) Channel {
	switch {
	case len(tag) > maxTagLen:
		return ChannelNone
	case stableTagRE.MatchString(tag):
		return ChannelStable
	case nightlyTagRE.MatchString(tag):
		return ChannelNightly
	}
	return ChannelNone
}

// ValidateTag accepts only tags that are safe to put in a URL or a path.
func ValidateTag(tag string) error {
	if ClassifyTag(tag) == ChannelNone {
		return fmt.Errorf("%w: %q", ErrInvalidTag, tag)
	}
	return nil
}

// newer orders releases newest first: nightlies by build number, which unlike the publication
// time follows the code, and anything else by publication time.
func newer(a, b Release) bool {
	if a.Channel == ChannelNightly && b.Channel == ChannelNightly {
		an, aerr := strconv.ParseUint(a.Tag[1:], 10, 64)
		bn, berr := strconv.ParseUint(b.Tag[1:], 10, 64)
		if aerr == nil && berr == nil {
			return an > bn
		}
	}
	return a.PublishedAt.After(b.PublishedAt)
}
