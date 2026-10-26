package providers

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// ForumTopicEventLinkResolver keeps observe-event URLs aligned with the live
// operator-owned site URL and topic URL mode.
type ForumTopicEventLinkResolver struct {
	options *options.Service
}

func NewForumTopicEventLinkResolver(optionsService *options.Service) ForumTopicEventLinkResolver {
	return ForumTopicEventLinkResolver{options: optionsService}
}

func (r ForumTopicEventLinkResolver) TopicEventURL(ctx context.Context, topic forum.TopicSummary) (string, error) {
	if r.options == nil {
		return "", fmt.Errorf("forum topic event link: options unavailable")
	}
	siteURL, err := r.options.WebOption(ctx, options.NameSiteURL)
	if err != nil {
		return "", err
	}
	mode, err := r.options.WebOption(ctx, options.NameSEOTopicURLMode)
	if err != nil {
		return "", err
	}
	return absoluteTopicEventURL(siteURL, mode, topic)
}

func absoluteTopicEventURL(siteURL, mode string, topic forum.TopicSummary) (string, error) {
	base, err := url.Parse(strings.TrimSpace(siteURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("forum topic event link: invalid site URL")
	}
	segments := []string{"t"}
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "id":
		segments = append(segments, strconv.FormatInt(topic.ID, 10))
	case "slug":
		if topic.Slug != "" {
			segments = append(segments, topic.Slug)
		} else {
			segments = append(segments, strconv.FormatInt(topic.ID, 10))
		}
	default:
		segments = append(segments, strconv.FormatInt(topic.ID, 10))
		if topic.Slug != "" {
			segments = append(segments, topic.Slug)
		}
	}
	base.RawQuery = ""
	base.Fragment = ""
	return url.JoinPath(base.String(), segments...)
}
