package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_setupTags(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    *resec
		wantErr error
	}{
		{
			name: "unique tags error",
			env: map[string]string{
				MasterTags: "ok,fine",
				SlaveTags:  "ok,fine",
			},
			want:    nil,
			wantErr: fmt.Errorf("[ERROR] The first tag in MASTER_TAGS and SLAVE_TAGS must be unique"),
		},
		{
			name: "unique tags ok",
			env: map[string]string{
				MasterTags: "something,fine",
				SlaveTags:  "else,fine",
			},
			want: &resec{
				consul: &consul{
					tags: map[string][]string{
						"master": []string{"something", "fine"},
						"slave":  []string{"else", "fine"},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				os.Setenv(k, v)
				defer os.Unsetenv(k)
			}

			assert := assert.New(t)

			got, err := setup()
			if tt.wantErr != nil {
				assert.EqualError(err, tt.wantErr.Error())
				return
			}

			if err != nil {
				assert.EqualError(err, "")
				return
			}

			if tt.wantErr == nil && err == nil && got == nil {
				return
			}

			assert.EqualValues(tt.want.consul.tags, got.consul.tags)
		})
	}
}
