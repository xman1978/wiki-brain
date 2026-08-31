package pptx

import (
	"encoding/xml"
	"path"
	"strings"
)

type relsXML struct {
	Relationships []relXML `xml:"Relationship"`
}

type relXML struct {
	ID     string `xml:"Id,attr"`
	Type   string `xml:"Type,attr"`
	Target string `xml:"Target,attr"`
}

type relationship struct {
	ID     string
	Type   string
	Target string
}

// parseRelationships parses an OOXML .rels part. Relative targets are joined
// against baseDir; absolute ("/"-rooted) targets are normalised to package-root
// relative paths (no leading slash) so they match zip entry names.
func parseRelationships(data []byte, baseDir string) (map[string]relationship, error) {
	var parsed relsXML
	if err := xml.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	out := make(map[string]relationship, len(parsed.Relationships))
	for _, r := range parsed.Relationships {
		target := r.Target
		if !strings.HasPrefix(target, "/") {
			target = path.Clean(path.Join(baseDir, target))
		} else {
			target = strings.TrimPrefix(path.Clean(target), "/")
		}
		out[r.ID] = relationship{ID: r.ID, Type: r.Type, Target: target}
	}
	return out, nil
}

// relsPathFor returns the .rels part path for a given part.
func relsPathFor(part string) string {
	dir, file := path.Split(part)
	return path.Join(dir, "_rels", file+".rels")
}
