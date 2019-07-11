package provisioner

import (
	"context"
	"errors"
	"io"

	"github.com/hashicorp/packer/packer"
	"github.com/hashicorp/packer/template/interpolate"

	"github.com/gen0cide/laforge/core/cli"
	"github.com/hashicorp/packer/helper/communicator"
	"github.com/hashicorp/packer/helper/multistep"
)

// New creates a new provisioner
func New(label string, c *communicator.Config, provconfig interface{}, stdin io.Reader, stdout io.Writer, stderr io.Writer) (Provisioner, error) {
	bag := &multistep.BasicStateBag{}
	ui := cli.NewUI(label)
	bag.Put("ui", ui)
	var p Provisioner
	var host string
	switch c.Type {
	case "ssh":
		p = &SSHProvisioner{
			Name:    label,
			Context: &interpolate.Context{},
		}
		c.Type = "ssh"
		host = c.SSHHost
	case "powershell":
		p = &PowershellProvisioner{
			Name:    label,
			Context: &interpolate.Context{},
		}
		c.Type = "winrm"
		host = c.WinRMHost
	case "windows-restart":
		p = &WindowsRestartProvisioner{
			Name:    label,
			Context: &interpolate.Context{},
		}
		c.Type = "winrm"
		host = c.WinRMHost
	case "windows-shell":
		p = &WindowsCmdProvisioner{
			Name:    label,
			Context: &interpolate.Context{},
		}
		c.Type = "winrm"
		host = c.WinRMHost
	default:
		return nil, errors.New("communicator configuration type unknown")
	}

	step := &communicator.StepConnect{
		Config: c,
		Host: func(m multistep.StateBag) (string, error) {
			return host, nil
		},
	}

	res := step.Run(context.TODO(), bag)
	if res != multistep.ActionContinue {
		return nil, errors.New("Connection attempt was unable to continue")
	}

	newcomm, ok := bag.GetOk("communicator")
	if !ok {
		return nil, errors.New("unable to create a new communicator")
	}

	p.SetUI(ui)
	err := p.SetConfig(provconfig)
	if err != nil {
		return nil, err
	}

	p.SetComms(newcomm.(packer.Communicator))
	p.SetIO(stdin, stdout, stderr)

	return p, nil
}
