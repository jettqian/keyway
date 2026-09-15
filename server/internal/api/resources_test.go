package api

import (
	"encoding/json"
	"testing"

	"keyway/internal/store"
)

// 空配置必须保持 JSON 数组/对象契约，供模型页和渠道编辑页直接使用。
func TestChannelDTOEmptyCollections(t *testing.T) {
	for _, raw := range []string{"null", "", "[]"} {
		t.Run(raw, func(t *testing.T) {
			dto := channelDTO(&store.Channel{ModelsJSON: raw, BaseURLsJSON: "null", KeyIDsJSON: "null", ModelMappingJSON: "null"})
			for _, field := range []string{"models", "baseUrls", "keyIds", "modelMapping"} {
				data, err := json.Marshal(dto[field])
				if err != nil {
					t.Fatal(err)
				}
				want := "[]"
				if field == "modelMapping" {
					want = "{}"
				}
				if string(data) != want {
					t.Errorf("%s = %s，期望 %s", field, data, want)
				}
			}
		})
	}
}
