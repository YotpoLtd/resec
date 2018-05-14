package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func Test_setup(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		want       *resec
		wantErr    bool
		wantErrMsg string
	}{
		{
			name: "unique tags",
			env: map[string]string{
				MasterTags: "ok,fine",
				SlaveTags:  "ok,fine",
			},
			want:       nil,
			wantErr:    true,
			wantErrMsg: "The first tag in MASTER_TAGS and SLAVE_TAGS must be unique",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				os.Setenv(k, v)
				defer os.Unsetenv(k)
			}

			got, err := setup()
			if (err != nil) != tt.wantErr {
				t.Errorf("setup() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil && !strings.Contains(err.Error(), tt.wantErrMsg) {
				t.Errorf("error = %v, wantErrMsg %v", err, tt.wantErrMsg)
				return
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("setup() = %v, want %v", got, tt.want)
			}
		})
	}
}
