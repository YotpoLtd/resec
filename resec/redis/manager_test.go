package redis

import (
	"strings"
	"testing"
	"time"

	"github.com/seatgeek/resec/resec/state"
	"github.com/stretchr/testify/assert"
)

func TestManager_parseReplicationResult(t *testing.T) {
	tests := []struct {
		name string
		str  string
		want state.RedisStatus
	}{
		{
			name: "empty",
			str:  "",
			want: state.RedisStatus{},
		},
		{
			name: "master - one slave",
			str: newlinefix(`
# Replication
role:master
connected_slaves:1
slave0:ip=127.0.0.1,port=9999,state=online,offset=14,lag=0
master_replid:fdf346471f13ff95adf5e768f5aa552e934259ab
master_replid2:0000000000000000000000000000000000000000
master_repl_offset:14
second_repl_offset:-1
repl_backlog_active:1
repl_backlog_size:1048576
repl_backlog_first_byte_offset:1
repl_backlog_histlen:14`),
			want: state.RedisStatus{
				Role: "master",
			},
		},
		{
			name: "slave - master up",
			str: newlinefix(`
# Replication
role:slave
master_host:127.0.0.1
master_port:6379
master_link_status:up
master_last_io_seconds_ago:7
master_sync_in_progress:0
slave_repl_offset:70
slave_priority:100
slave_read_only:1
connected_slaves:0
master_replid:fdf346471f13ff95adf5e768f5aa552e934259ab
master_replid2:0000000000000000000000000000000000000000
master_repl_offset:70
second_repl_offset:-1
repl_backlog_active:1
repl_backlog_size:1048576
repl_backlog_first_byte_offset:1
repl_backlog_histlen:70`),
			want: state.RedisStatus{
				Role:         "slave",
				MasterLinkUp: true,
				MasterHost:   "127.0.0.1",
				MasterPort:   6379,
			},
		},
		{
			name: "slave - master down",
			str: newlinefix(`
# Replication
role:slave
master_host:127.0.0.1
master_port:6379
master_link_status:down
master_last_io_seconds_ago:-1
master_sync_in_progress:0
slave_repl_offset:32102871
master_link_down_since_seconds:35
slave_priority:100
slave_read_only:1
connected_slaves:0
master_replid:65731058a8ddc1d178d9cb7f8a3a8d41c224af41
master_replid2:0000000000000000000000000000000000000000
master_repl_offset:32102871
second_repl_offset:-1
repl_backlog_active:1
repl_backlog_size:1048576
repl_backlog_first_byte_offset:32101346
repl_backlog_histlen:1526`),
			want: state.RedisStatus{
				Role:                "slave",
				MasterLinkUp:        false,
				MasterLinkDownSince: 35 * time.Second,
				MasterHost:          "127.0.0.1",
				MasterPort:          6379,
			},
		},
		{
			name: "slave - slave sync in progress",
			str: newlinefix(`
# Replication
role:slave
master_host:127.0.0.1
master_port:6379
master_link_status:down
master_last_io_seconds_ago:-1
master_sync_in_progress:1
slave_repl_offset:32102871
master_link_down_since_seconds:35
slave_priority:100
slave_read_only:1
connected_slaves:0
master_replid:65731058a8ddc1d178d9cb7f8a3a8d41c224af41
master_replid2:0000000000000000000000000000000000000000
master_repl_offset:32102871
second_repl_offset:-1
repl_backlog_active:1
repl_backlog_size:1048576
repl_backlog_first_byte_offset:32101346
repl_backlog_histlen:1526`),
			want: state.RedisStatus{
				Role:                 "slave",
				MasterLinkUp:         false,
				MasterSyncInProgress: true,
				MasterLinkDownSince:  35 * time.Second,
				MasterHost:           "127.0.0.1",
				MasterPort:           6379,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Manager{}
			assert.Equal(t, tt.want, m.parseInfoResult(tt.str))
		})
	}
}

func newlinefix(s string) string {
	return strings.Replace(s, "\n", "\r\n", -1)
}
