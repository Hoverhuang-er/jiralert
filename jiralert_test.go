package jiralert

import (
	"github.com/Hoverhuang-er/jiralert/cmd"
	"testing"
)

func Test_main(t *testing.T) {
	tests := []struct {
		name string
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd.main()
		})
	}
}
