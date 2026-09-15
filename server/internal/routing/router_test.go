package routing

import (
	"fmt"
	"testing"

	"keyway/internal/store"
)

// 令牌限定渠道后，候选必须按令牌绑定顺序（令牌级优先级）排列；
// 未限定时保持渠道 priority 降序。
func TestResolveTokenChannelOrder(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, "secret")

	newKey := func(name string) *store.Key {
		k := &store.Key{UserID: 1, Name: name, ValueEnc: []byte("enc"), Status: 1}
		if err := st.DB().Create(k).Error; err != nil {
			t.Fatal(err)
		}
		return k
	}
	newChannel := func(name string, priority int, keyID int64) *store.Channel {
		ch := &store.Channel{
			UserID: 1, Name: name, Type: "", Enabled: 1, Priority: priority,
			BaseURLsJSON: fmt.Sprintf(`["https://%s.example.com"]`, name),
			KeyIDsJSON:   fmt.Sprintf(`[%d]`, keyID),
			ModelsJSON:   `["m"]`,
		}
		if err := st.DB().Create(ch).Error; err != nil {
			t.Fatal(err)
		}
		return ch
	}

	c1 := newChannel("c1", 30, newKey("k1").ID) // 全局优先级最高
	c2 := newChannel("c2", 20, newKey("k2").ID)
	c3 := newChannel("c3", 10, newKey("k3").ID)

	names := func(list []*ResolvedChannel) string {
		out := ""
		for _, rc := range list {
			if out != "" {
				out += ","
			}
			out += rc.Channel.Name
		}
		return out
	}

	matched, _, err := svc.Resolve(1, "m", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(matched); got != "c1,c2,c3" {
		t.Errorf("不限渠道时期望按优先级 c1,c2,c3，实际 %s", got)
	}

	matched, _, err = svc.Resolve(1, "m", []int64{c3.ID, c1.ID, c2.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(matched); got != "c3,c1,c2" {
		t.Errorf("令牌限定 [c3,c1,c2] 时期望按令牌顺序 c3,c1,c2，实际 %s", got)
	}
}
