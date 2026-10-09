package store

import (
	"context"
	"reflect"
	"testing"
)

func TestModelProtocolUpgradeAndLegacyWrites(t *testing.T) {
	db, ctx := openModelChannelTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`ALTER TABLE models DROP COLUMN protocol`); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, item := range []struct{ id, typ, format, kind, base, want string }{
		{"chat", "openai", "chat", "chat", "", "openai.chat"},
		{"responses", "openai", "responses", "chat", "", "openai.responses"},
		{"anthropic", "claude", "", "chat", "", "anthropic.messages"},
		{"gemini", "gemini", "", "chat", "", "gemini.generateContent"},
		{"image", "openai", "responses", "image", "", "openai.images"},
		{"gemini-image", "google", "", "image", "", "gemini.generateContent"},
		{"embedding", "claude", "", "embedding", "", "openai.embeddings"},
		{"aliyun-compatible", "openai", "chat", "embedding", "https://llm-qbihp8zf48j4xmoj.cn-beijing.maas.aliyuncs.com/compatible-mode", "openai.embeddings"},
		{"dashscope", "openai", "", "embedding", "https://dashscope.aliyuncs.com/api/v1", "dashscope.embeddings"},
		{"decision", "typesafe", "", "decision", "", "typesafe.decisions"},
	} {
		if _, err := db.Exec(`INSERT INTO channels(id,name,type,api_format,base_url) VALUES(?,?,?,?,?)`, item.id, item.id, item.typ, item.format, item.base); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES(?,?,?,?,?)`, item.id, item.id, item.kind, item.id, item.id); err != nil {
			t.Fatal(err)
		}
		want[item.id] = item.want
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	models, err := ListModels(ctx, db, "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		if model.Protocol != want[model.ID] {
			t.Errorf("%s protocol = %s, want %s", model.ID, model.Protocol, want[model.ID])
		}
	}
	if _, err := db.Exec(`UPDATE channels SET type='openai',api_format='chat'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	model, err := GetModel(ctx, db, "anthropic")
	if err != nil || model.Protocol != "anthropic.messages" {
		t.Fatalf("channel change altered protocol: %+v, %v", model, err)
	}
	// An older binary can still write a row without the new column.
	if _, err := db.Exec(`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('legacy','responses','chat','legacy','Legacy')`); err != nil {
		t.Fatal(err)
	}
	model, err = GetModel(ctx, db, "legacy")
	if err != nil || model.Protocol != "openai.chat" {
		t.Fatalf("legacy write unreadable: %+v, %v", model, err)
	}
}

func TestModelProtocolIsIndependentOfPriorityChannels(t *testing.T) {
	db, ctx := openModelChannelTestDB(t)
	defer db.Close()
	for _, id := range []string{"a", "b"} {
		if _, err := db.Exec(`INSERT INTO channels(id,name,type,api_format,api_key) VALUES(?,?,'claude','','key')`, id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := ReplaceChannelModels(ctx, db, id, []ChannelModel{{RequestID: "test", Kind: "chat", Enabled: true}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE channels SET type='google',api_format='responses' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	m, err := CreateModel(ctx, db, Model{ChannelID: "a", Kind: "chat", Protocol: "openai.responses", RequestID: "test", Label: "Test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaceModelChannelBindings(ctx, db, m, []ModelChannelBinding{{ChannelID: "a", Priority: 1, Weight: 100}, {ChannelID: "b", Priority: 2, Weight: 100}}, nil); err != nil {
		t.Fatal(err)
	}
	ids, err := ModelChannelCandidateIDs(ctx, db, m.ID, m.RequestID)
	if err != nil || !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("candidates = %v, %v", ids, err)
	}
	for _, id := range ids {
		channel, err := GetModelChannel(context.Background(), db, m, id)
		if err != nil || channel.Type != "openai" || channel.APIFormat != "responses" {
			t.Fatalf("request channel = %+v, %v", channel, err)
		}
	}
	for _, item := range []Model{{Kind: "embedding", Protocol: "openai.responses"}, {Kind: "chat", Protocol: "typesafe.decisions"}, {Kind: "image", Protocol: "anthropic.messages"}, {Kind: "chat", Protocol: "unknown"}} {
		if ValidateModelProtocol(&item) == nil {
			t.Fatalf("accepted invalid model: %+v", item)
		}
	}
}
