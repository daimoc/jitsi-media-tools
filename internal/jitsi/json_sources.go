package jitsi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Jicofo sends remote sources to clients advertising
// http://jitsi.org/json-encoded-sources as a compact JSON document in a
// <json-message/> of session-initiate, source-add and source-remove, instead
// of <source/> and <ssrc-group/> elements (Jicofo's ConferenceSourceMap
// compactJson):
//
//	{"sources": {"<owner>": [[video sources], [video groups], [audio sources], [audio groups]]}}
//
// The audio groups list is omitted when empty. A source is
// {"s": ssrc, "n": name, "m": msid, "v": "d"} ("v" only for desktop video),
// a group ["f" | "s", ssrc, ...] (FID or SIM). The owner is the occupant
// nick, or "jvb" for the bridge's own sources.

// jsonSource is a compact JSON source.
type jsonSource struct {
	SSRC uint32 `json:"s"`
	Name string `json:"n"`
	MSID string `json:"m"`
	V    string `json:"v"`
}

// applyJSONSources converts the json-message of j, if any, to Jingle sources
// and SSRC groups in its audio and video contents (created if missing), so
// that the rest of the adapter handles both encodings alike.
func applyJSONSources(j *JingleIQ) error {
	if j.JSONMessage == nil {
		return nil
	}
	owners, err := decodeJSONSources(j.JSONMessage.Text)
	if err != nil {
		return fmt.Errorf("invalid json-message: %w", err)
	}

	for _, o := range owners {
		owner, lists := o.owner, o.lists
		for i, media := range []string{"video", "audio"} {
			var sources []jsonSource
			var groups []json.RawMessage
			if 2*i < len(lists) {
				if err := json.Unmarshal(lists[2*i], &sources); err != nil {
					return fmt.Errorf("invalid %s sources of %s: %w", media, owner, err)
				}
			}
			if 2*i+1 < len(lists) {
				if err := json.Unmarshal(lists[2*i+1], &groups); err != nil {
					return fmt.Errorf("invalid %s groups of %s: %w", media, owner, err)
				}
			}
			if len(sources) == 0 && len(groups) == 0 {
				continue
			}
			desc := descriptionFor(j, media)
			for _, s := range sources {
				desc.Sources = append(desc.Sources, s.toSource(media, owner))
			}
			for _, raw := range groups {
				g, err := parseJSONGroup(raw)
				if err != nil {
					return fmt.Errorf("invalid %s group of %s: %w", media, owner, err)
				}
				desc.SSRCGroups = append(desc.SSRCGroups, g)
			}
		}
	}
	return nil
}

func (s jsonSource) toSource(media, owner string) Source {
	src := Source{SSRC: strconv.FormatUint(uint64(s.SSRC), 10), Name: s.Name, SSRCInfo: &SSRCInfo{Owner: owner}}
	if s.MSID != "" {
		src.Parameters = []Parameter{{Name: "msid", Value: s.MSID}}
	}
	if media == "video" {
		src.VideoType = "camera"
		if s.V == "d" {
			src.VideoType = "desktop"
		}
	}
	return src
}

// ownerSources is one owner's lists, in document order.
type ownerSources struct {
	owner string
	lists []json.RawMessage
}

// decodeJSONSources reads {"sources": {owner: lists, ...}} keeping the
// owners in document order (the order Jicofo announces them, which the
// receive cap follows).
func decodeJSONSources(text string) ([]ownerSources, error) {
	var doc struct {
		Sources json.RawMessage `json:"sources"`
	}
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return nil, err
	}
	if len(doc.Sources) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(doc.Sources))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("sources: expected an object")
	}
	var out []ownerSources
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		owner, _ := tok.(string)
		var lists []json.RawMessage
		if err := dec.Decode(&lists); err != nil {
			return nil, fmt.Errorf("sources of %s: %w", owner, err)
		}
		out = append(out, ownerSources{owner: owner, lists: lists})
	}
	return out, nil
}

// parseJSONGroup parses ["f" | "s", ssrc, ...].
func parseJSONGroup(raw json.RawMessage) (SSRCGroup, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || len(items) < 2 {
		return SSRCGroup{}, fmt.Errorf("expected [semantics, ssrc...], got %s", raw)
	}
	var semantics string
	if err := json.Unmarshal(items[0], &semantics); err != nil {
		return SSRCGroup{}, err
	}
	g := SSRCGroup{}
	switch semantics {
	case "f":
		g.Semantics = "FID"
	case "s":
		g.Semantics = "SIM"
	default:
		return SSRCGroup{}, fmt.Errorf("unknown group semantics %q", semantics)
	}
	for _, item := range items[1:] {
		var ssrc uint32
		dec := json.NewDecoder(bytes.NewReader(item))
		if err := dec.Decode(&ssrc); err != nil {
			return SSRCGroup{}, err
		}
		g.Sources = append(g.Sources, SSRCGroupID{SSRC: strconv.FormatUint(uint64(ssrc), 10)})
	}
	return g, nil
}

// descriptionFor returns the RTP description of j's content for media,
// creating the content if needed (source-add and source-remove carry none).
func descriptionFor(j *JingleIQ, media string) *JingleDescription {
	for i := range j.Content {
		c := &j.Content[i]
		if c.Name == media || (c.Description != nil && c.Description.Media == media) {
			if c.Description == nil {
				c.Description = &JingleDescription{Media: media}
			}
			return c.Description
		}
	}
	j.Content = append(j.Content, JingleContent{Creator: "initiator", Name: media, Description: &JingleDescription{Media: media}})
	return j.Content[len(j.Content)-1].Description
}
