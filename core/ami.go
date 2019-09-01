package core

import (
	"fmt"

	"github.com/gen0cide/laforge/core/formatter"
)

// AMI represents a configurable object for defining custom AMIs in cloud infrastructure
//easyjson:json
type AMI struct {
	formatter.Formatable
	ID          string            `hcl:"id,label" json:"id,omitempty"`
	Name        string            `hcl:"name,attr" json:"name,omitempty"`
	Description string            `hcl:"description,attr" json:"description,omitempty"`
	Provider    string            `hcl:"provider,attr" json:"provider,omitempty"`
	Username    string            `hcl:"username,attr" json:"username,omitempty"`
	Vars        map[string]string `hcl:"vars,optional" json:"vars,omitempty"`
	Tags        map[string]string `hcl:"tags,optional" json:"tags,omitempty"`
	Maintainer  *User             `hcl:"maintainer,block" json:"maintainer,omitempty"`
}

func (a AMI) ToString() string {
	return fmt.Sprintf(`AMI
┠ ID (string)          = %s
┠ Name (string)        = %s
┠ Description (string) = %s
┠ Provider (string)    = %s
┠ Username (string)    = %s
┠ Vars (map)
%s
┗ Tags (map)
%s
`,
		a.ID,
		a.Name,
		a.Description,
		a.Provider,
		a.Username,
		formatter.FormatStringMap(a.Vars),
		formatter.FormatStringMap(a.Tags))
}

// We have no children on a DNSRecord, so nothing to iterate on, we'll just return
func (a AMI) Iter() ([]formatter.Formatable, error) {
	return []formatter.Formatable{}, nil
}
