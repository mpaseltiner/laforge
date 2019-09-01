package core

import (
	"fmt"

	"github.com/cespare/xxhash"
	"github.com/gen0cide/laforge/core/formatter"
)

// Remote defines a configuration object that keeps terraform and remote files synchronized
//easyjson:json
type Remote struct {
	formatter.Formatable
	ID     string            `hcl:"id,label" json:"id,omitempty"`
	Type   string            `hcl:"type,attr" json:"type,omitempty"`
	Config map[string]string `hcl:"config,optional" json:"config,omitempty"`
}

func (r Remote) ToString() string {
	return fmt.Sprintf(`Remote
┠ ID (string)   = %s
┠ Type (string) = %s
┗ Config (map)
%s
`,
		r.ID,
		r.Type,
		formatter.FormatStringMap(r.Config))
}

// We have no children on a DNSRecord, so nothing to iterate on, we'll just return
func (r Remote) Iter() ([]formatter.Formatable, error) {
	return []formatter.Formatable{}, nil
}

// Hash implements the Hasher interface
func (r *Remote) Hash() uint64 {
	return xxhash.Sum64String(
		fmt.Sprintf(
			"id=%v type=%v config=%v",
			r.ID,
			r.Type,
			r.Config,
		),
	)
}
