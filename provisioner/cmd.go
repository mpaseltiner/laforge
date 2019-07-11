package provisioner

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/packer/common"
	"github.com/hashicorp/packer/packer"
	winshell "github.com/hashicorp/packer/provisioner/windows-shell"
	"github.com/hashicorp/packer/template/interpolate"
)

// WindowsCmdProvisioner implements a communicator to interact with a host via SSH
type WindowsCmdProvisioner struct {
	Name              string
	Comm              packer.Communicator
	Config            *winshell.Config
	UI                packer.Ui
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
	StartRetryTimeout time.Duration
	Context           *interpolate.Context
	cancel            chan struct{}
	cancelLock        sync.Mutex
}

// SetName implements the Provisioner interface
func (p *WindowsCmdProvisioner) SetName(s string) {
	p.Name = s
}

// GetName implements the Provisioner interface
func (p *WindowsCmdProvisioner) GetName() string {
	return p.Name
}

// SetUI implements the Provisioner interface
func (p *WindowsCmdProvisioner) SetUI(ui packer.Ui) {
	p.UI = ui
}

// GetUI implements the Provisioner interface
func (p *WindowsCmdProvisioner) GetUI() packer.Ui {
	return p.UI
}

// SetConfig implements the Provisioner interface
func (p *WindowsCmdProvisioner) SetConfig(c interface{}) error {
	sc, ok := c.(*winshell.Config)
	if !ok {
		return errors.New("config is not of type *shell.Config")
	}
	p.Config = sc
	return p.Prepare(sc)
}

// GetConfig implements the Provisioner interface
func (p *WindowsCmdProvisioner) GetConfig() interface{} {
	return p.Config
}

// SetComms implements the Provisioner interface
func (p *WindowsCmdProvisioner) SetComms(c packer.Communicator) {
	p.Comm = c
}

// GetComms implements the Provisioner interface
func (p *WindowsCmdProvisioner) GetComms() packer.Communicator {
	return p.Comm
}

// SetIO implements the Provisioner interface
func (p *WindowsCmdProvisioner) SetIO(in io.Reader, out io.Writer, err io.Writer) {
	p.Stdin = in
	p.Stdout = out
	p.Stderr = err
}

// GetIO implements the Provisioner interface
func (p *WindowsCmdProvisioner) GetIO() (io.Reader, io.Writer, io.Writer) {
	return p.Stdin, p.Stdout, p.Stderr
}

// Prepare implements the provisioenr interface
func (p *WindowsCmdProvisioner) Prepare(raws ...interface{}) error {
	if p.Config.EnvVarFormat == "" {
		p.Config.EnvVarFormat = `set "%s=%s" && `
	}

	if p.Config.ExecuteCommand == "" {
		p.Config.ExecuteCommand = `{{.Vars}}"{{.Path}}"`
	}

	if p.Config.Inline != nil && len(p.Config.Inline) == 0 {
		p.Config.Inline = nil
	}

	if p.Config.StartRetryTimeout == 0 {
		p.Config.StartRetryTimeout = 5 * time.Minute
	}

	if p.Config.RemotePath == "" {
		p.Config.RemotePath = winshell.DefaultRemotePath
	}

	if p.Config.Scripts == nil {
		p.Config.Scripts = make([]string, 0)
	}

	if p.Config.Vars == nil {
		p.Config.Vars = make([]string, 0)
	}

	var errs error
	if p.Config.Script != "" && len(p.Config.Scripts) > 0 {
		errs = packer.MultiErrorAppend(errs, errors.New("Only one of script or scripts can be specified"))
	}

	if p.Config.Script != "" {
		p.Config.Scripts = []string{p.Config.Script}
	}

	if len(p.Config.Scripts) == 0 && p.Config.Inline == nil {
		errs = packer.MultiErrorAppend(errs, errors.New("Either a script file or inline script must be specified"))
	} else if len(p.Config.Scripts) > 0 && p.Config.Inline != nil {
		errs = packer.MultiErrorAppend(errs, errors.New("Only a script file or an inline script can be specified, not both"))
	}

	for _, path := range p.Config.Scripts {
		if _, err := os.Stat(path); err != nil {
			errs = packer.MultiErrorAppend(errs, fmt.Errorf("Bad script '%s': %s", path, err))
		}
	}

	// Do a check for bad environment variables, such as '=foo', 'foobar'
	for _, kv := range p.Config.Vars {
		vs := strings.SplitN(kv, "=", 2)
		if len(vs) != 2 || vs[0] == "" {
			errs = packer.MultiErrorAppend(errs, fmt.Errorf("Environment variable not in format 'key=value': %s", kv))
		}
	}

	return errs
}

// This function takes the inline scripts, concatenates them
// into a temporary file and returns a string containing the location
// of said file.
func extractCmdScript(p *WindowsCmdProvisioner) (string, error) {
	temp, err := ioutil.TempFile(os.TempDir(), "packer-windows-shell-provisioner")
	if err != nil {
		log.Printf("Unable to create temporary file for inline scripts: %s", err)
		return "", err
	}
	writer := bufio.NewWriter(temp)
	for _, command := range p.Config.Inline {
		log.Printf("Found command: %s", command)
		if _, err := writer.WriteString(command + "\n"); err != nil {
			return "", fmt.Errorf("Error preparing shell script: %s", err)
		}
	}

	if err := writer.Flush(); err != nil {
		return "", fmt.Errorf("Error preparing shell script: %s", err)
	}

	temp.Close()

	return temp.Name(), nil
}

// Provision implements the provisioner interface
func (p *WindowsCmdProvisioner) Provision() error {
	p.UI.Say(fmt.Sprintf("Provisioning with windows-shell..."))
	scripts := make([]string, len(p.Config.Scripts))
	copy(scripts, p.Config.Scripts)

	if p.Config.Inline != nil {
		temp, err := extractCmdScript(p)
		if err != nil {
			p.UI.Error(fmt.Sprintf("Unable to extract inline scripts into a file: %s", err))
		}
		scripts = append(scripts, temp)
		// Remove temp script containing the inline commands when done
		defer os.Remove(temp)
	}

	for _, path := range scripts {
		p.UI.Say(fmt.Sprintf("Provisioning with shell script: %s", path))

		log.Printf("Opening %s for reading", path)
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("Error opening shell script: %s", err)
		}
		defer f.Close()

		// Create environment variables to set before executing the command
		flattenedVars := p.createFlattenedEnvVars()

		// Compile the command
		p.Context.Data = &ExecuteCommandTemplate{
			Vars: flattenedVars,
			Path: p.Config.RemotePath,
		}
		command, err := interpolate.Render(p.Config.ExecuteCommand, p.Context)
		if err != nil {
			return fmt.Errorf("Error processing command: %s", err)
		}

		// Upload the file and run the command. Do this in the context of
		// a single retryable function so that we don't end up with
		// the case that the upload succeeded, a restart is initiated,
		// and then the command is executed but the file doesn't exist
		// any longer.
		var cmd *packer.RemoteCmd
		err = p.retryable(func() error {
			if _, err := f.Seek(0, 0); err != nil {
				return err
			}

			if err := p.Comm.Upload(p.Config.RemotePath, f, nil); err != nil {
				return fmt.Errorf("Error uploading script: %s", err)
			}

			cmd = &packer.RemoteCmd{
				Stdin:   p.Stdin,
				Stdout:  p.Stdout,
				Stderr:  p.Stderr,
				Command: command,
			}
			debugLine := fmt.Sprintf("%v - %s", time.Now(), cmd.Command)
			fmt.Fprintf(p.Stdout, "##### >>> %s\n", debugLine)
			fmt.Fprintf(p.Stderr, "##### >>> %s\n", debugLine)
			return cmd.StartWithUi(p.Comm, p.UI)
		})
		if err != nil {
			return err
		}

		// Close the original file since we copied it
		f.Close()

		if cmd.ExitStatus != 0 {
			return fmt.Errorf("Script exited with non-zero exit status: %d", cmd.ExitStatus)
		}

		err = p.cleanupRemoteFile(p.Config.RemotePath, p.Comm)
		if err != nil {
			return err
		}
	}

	return nil
}

func (p *WindowsCmdProvisioner) cleanupRemoteFile(path string, comm packer.Communicator) error {
	err := p.retryable(func() error {
		cmd := &packer.RemoteCmd{
			Stdin:   p.Stdin,
			Stdout:  p.Stdout,
			Stderr:  p.Stderr,
			Command: fmt.Sprintf("del /f %s", path),
		}
		debugLine := fmt.Sprintf("%v - %s", time.Now(), cmd.Command)
		fmt.Fprintf(p.Stdout, "##### >>> %s\n", debugLine)
		fmt.Fprintf(p.Stderr, "##### >>> %s\n", debugLine)
		if err := comm.Start(cmd); err != nil {
			return fmt.Errorf("Error removing temporary script at %s: %s", path, err)
		}
		cmd.Wait()
		// treat disconnects as retryable by returning an error
		if cmd.ExitStatus == packer.CmdDisconnect {
			return fmt.Errorf("disconnect while removing temporary script")
		}
		if cmd.ExitStatus != 0 {
			return fmt.Errorf("error removing temporary script at %s", path)
		}
		return nil
	})

	if err != nil {
		return err
	}

	return nil
}

// Cancel implements the provisioner interface
func (p *WindowsCmdProvisioner) Cancel() {
	return
}

// retryable will retry the given function over and over until a
// non-error is returned.
func (p *WindowsCmdProvisioner) retryable(f func() error) error {
	startTimeout := time.After(p.Config.StartRetryTimeout)
	for {
		var err error
		if err = f(); err == nil {
			return nil
		}

		// Create an error and log it
		err = fmt.Errorf("Retryable error: %s", err)
		log.Print(err.Error())

		// Check if we timed out, otherwise we retry. It is safe to
		// retry since the only error case above is if the command
		// failed to START.
		select {
		case <-startTimeout:
			return err
		default:
			time.Sleep(retryableSleep)
		}
	}
}

func (p *WindowsCmdProvisioner) createFlattenedEnvVars() (flattened string) {
	flattened = ""
	envVars := make(map[string]string)

	// Always available Packer provided env vars
	envVars["PACKER_BUILD_NAME"] = p.Config.PackerBuildName
	envVars["PACKER_BUILDER_TYPE"] = p.Config.PackerBuilderType
	httpAddr := common.GetHTTPAddr()
	if httpAddr != "" {
		envVars["PACKER_HTTP_ADDR"] = httpAddr
	}

	// Split vars into key/value components
	for _, envVar := range p.Config.Vars {
		keyValue := strings.SplitN(envVar, "=", 2)
		envVars[keyValue[0]] = keyValue[1]
	}
	// Create a list of env var keys in sorted order
	var keys []string
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Re-assemble vars using OS specific format pattern and flatten
	for _, key := range keys {
		flattened += fmt.Sprintf(p.Config.EnvVarFormat, key, envVars[key])
	}
	return
}
