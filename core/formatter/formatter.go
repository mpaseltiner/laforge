package formatter

import (
	"fmt"
	"strings"
)

// Formatable is a type that can be implemented by various LaForge types to provide us a standard
// way to get high level information about that object and the data it contains.
type Formatable interface {
	/* ToString returns a string representation of the current type, including all of the
	properties and values.  This will be useful to allow us to see every variable available
	inside a Context for scripting, but will also have other uses to help end users

	Parameters: none
	Returns:
		[]string - A multiline string representing the current item
		error    - Any errors that we may have, will be nil if successful
	*/
	ToString() string

	/* Iter returns a list of all children of this type that are also Formattable, allowing us to
	iterate through them as necessary to get additional data, including string representations.

	Parameters: none
	Returns:
		[]Formatable - A slice of all children which can also be formatted
		error        - Any errors that we may have encountered, will be nil if successful
	*/
	Iter() ([]Formatable, error)
}

// Formatter takes Formatable types and gets some nice pretty output in the form of a string slice,
// one line per element.
type Formatter struct {
}

// GetStrings takes a single Formatable type and will recursively move through both that head and
// it's children, creating a string based representation of all properties of this item
func (this *Formatter) GetStrings(head Formatable, maxDepth, curDepth int) (string, error) {
	var outData strings.Builder // To hold all fof our output

	outHead := head.ToString()                     // Get the string of the head
	outData.WriteString(this.FormatChild(outHead)) // Merge the strings together

	kidsHead, err := head.Iter()
	if err != nil { // If there's an error, let's return what we've got thus far and the error
		return outData.String(), err
	}

	tmpErr := []error{} // While processing through all children we may get errors, we need to be prepared
	for _, v := range kidsHead {
		cur, err := this.GetStrings(v, maxDepth, curDepth+1)

		if err != nil {
			tmpErr = append(tmpErr, err) // For now, we'll add it to the slice
			continue                     // And move on to our next item
		}

		outData.WriteString(cur)
	}

	return outData.String(), nil
}

// FormatChild takes a string and adds characters in front of it to show the depth of the
// child in the output we are generating
func (this Formatter) FormatChild(child string) string {
	tmpData := strings.Split(child, "\n")

	for k, v := range child {
		tmpData[k] = fmt.Sprintf(" ┃ %s", v)
	}

	return strings.Join(tmpData, "\n")
}

func FormatStringSlice(cur []string) string {
	var out strings.Builder // A place to hold our output

	l := len(cur)           // For efficiency sake, we'll get our length once
	for k, v := range cur { // Loop through every value
		if (k + 1) == l { // If we're on the last item we use a different set of characters
			out.WriteString(fmt.Sprintf("┃ ┣ (string) %s\n", v))
		} else {
			out.WriteString(fmt.Sprintf("┃ ┗ (string) %s\n", v))
		}
	}

	return out.String()
}

func FormatStringMap(cur map[string]string) string {
	var out strings.Builder // A place to hold our output

	for k, v := range cur { // Loop through every value
		out.WriteString(fmt.Sprintf("┃ ┣ key: %s = (string) %s\n", k, v))
	}

	return out.String()
}
