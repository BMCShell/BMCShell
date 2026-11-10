package render

import (
	"fmt"
	"io"
	"strings"
)

type Topic struct {
	Summary  string
	Usage    []string
	Options  []struct{ Name, Description, Default string }
	Examples []struct{ Command, Description string }
	Notes    []string
}

func Help(out io.Writer, topic Topic, detailed bool) {
	fmt.Fprintln(out, "说明")
	block(out, topic.Summary)
	if !detailed {
		return
	}
	if len(topic.Usage) != 0 {
		fmt.Fprintln(out, "\n用法")
		for _, line := range topic.Usage {
			block(out, line)
		}
	}
	if len(topic.Options) != 0 {
		fmt.Fprintln(out, "\n参数与默认值")
		for _, option := range topic.Options {
			block(out, option.Name+"  "+option.Description)
			if option.Default != "" {
				block(out, "  默认值 "+option.Default)
			}
		}
	}
	if len(topic.Examples) != 0 {
		fmt.Fprintln(out, "\n示例")
		for _, example := range topic.Examples {
			if example.Description != "" {
				block(out, example.Description)
			}
			block(out, example.Command)
		}
	}
	if len(topic.Notes) != 0 {
		fmt.Fprintln(out, "\n使用说明")
		for _, note := range topic.Notes {
			block(out, note)
		}
	}
}

func block(out io.Writer, value string) {
	for _, line := range strings.Split(value, "\n") {
		fmt.Fprintln(out, "  "+line)
	}
}
