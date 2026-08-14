package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAdminGeneratedImagesUseAuthorPromptAndImageUsageModel(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "admin-generated-images.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	exec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES
		('owner','owner@example.test','Owner','h','user','active'),
		('artist','artist@example.test','Image Artist','h','user','active')`)
	exec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch','Images','openai')`)
	exec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label) VALUES
		('chat-model','ch','chat','chat','Chat Model'),
		('image-model','ch','image','image','Image Model')`)
	exec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('conv','owner','Shared drawing')`)
	exec(t, db, `INSERT INTO messages(id,conversation_id,role,blocks,search_text,author_id)
		VALUES('prompt','conv','user','[]','Draw a glass observatory','artist')`)
	exec(t, db, `INSERT INTO messages(id,conversation_id,parent_id,role,model_id,model_label,blocks,author_id)
		VALUES('answer','conv','prompt','assistant','chat-model','Chat Model',
		'[{"kind":"tool_call","tool_name":"image_generate"},{"kind":"tool_call","tool_name":"python_execute"}]','artist')`)
	exec(t, db, `INSERT INTO artifacts(id,message_id,filename,storage_path,mime_type,size_bytes,source,created_at) VALUES
		('artifact','answer','observatory.png','/tmp/observatory.png','image/png',2048,'image_generate',100),
		('python-chart','answer','chart.png','/tmp/chart.png','image/png',1024,'python_execute',101)`)
	exec(t, db, `INSERT INTO billing_usage(id,user_id,conversation_id,message_id,model_id,purpose,images_count,created_at)
		VALUES('usage','artist','conv','answer','image-model','image',1,100)`)

	ctx := context.Background()
	items, err := ListAdminGeneratedImages(ctx, db, AdminGeneratedImageFilter{UserQ: "ARTIST@EXAMPLE"}, 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d, want 1", len(items))
	}
	image := items[0]
	if image.UserID != "artist" || image.UserName != "Image Artist" {
		t.Fatalf("owner=%q/%q, want artist/Image Artist", image.UserID, image.UserName)
	}
	if image.ModelID != "image-model" || image.ModelLabel != "Image Model" {
		t.Fatalf("model=%q/%q, want image-model/Image Model", image.ModelID, image.ModelLabel)
	}
	if image.Prompt != "Draw a glass observatory" {
		t.Fatalf("prompt=%q", image.Prompt)
	}
	if image.ID != "artifact" {
		t.Fatalf("image id=%q, python_execute output entered admin gallery", image.ID)
	}

	total, err := CountAdminGeneratedImages(ctx, db, AdminGeneratedImageFilter{ModelID: "image-model"})
	if err != nil || total != 1 {
		t.Fatalf("count=%d err=%v, want 1", total, err)
	}
	models, err := ListAdminGeneratedImageModels(ctx, db)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "image-model" || models[0].Label != "Image Model" {
		t.Fatalf("models=%+v", models)
	}

	// Deleting the image catalog entry must not relabel the durable image usage
	// with the assistant turn's chat-model snapshot.
	exec(t, db, `DELETE FROM models WHERE id='image-model'`)
	items, err = ListAdminGeneratedImages(ctx, db, AdminGeneratedImageFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("list after model delete: %v", err)
	}
	if len(items) != 1 || items[0].ModelLabel != "image-model" {
		t.Fatalf("deleted model fallback=%+v", items)
	}
}

func TestGeneratedImageGalleriesRejectAllUnattributedLegacyImages(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-gallery-provenance.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	exec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status)
		VALUES('artist','artist@example.test','Artist','h','user','active')`)
	exec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch','Images','openai')`)
	exec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label) VALUES
		('chat-model','ch','chat','chat','Chat Model'),
		('image-model','ch','image','image','Image Model')`)
	exec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('conv','artist','Legacy outputs')`)
	exec(t, db, `INSERT INTO messages(id,conversation_id,role,model_id,blocks,raw,author_id) VALUES
		('legacy-image','conv','assistant','chat-model','[]','', 'artist'),
		('legacy-mixed','conv','assistant','chat-model','[]','', 'artist'),
		('legacy-python','conv','assistant','chat-model',
		 '[{"kind":"tool_call","tool_name":"python_execute"}]',
		 '[{"type":"function_call","name":"python_execute"}]','artist')`)
	exec(t, db, `INSERT INTO artifacts(id,message_id,filename,storage_path,mime_type,size_bytes,source,created_at) VALUES
		('legacy-generated','legacy-image','generated.png','/tmp/generated.png','image/png',10,'',100),
		('legacy-mixed-generated','legacy-mixed','generated-2.png','/tmp/generated-2.png','image/png',10,'',101),
		('legacy-mixed-python','legacy-mixed','chart-2.png','/tmp/chart-2.png','image/png',10,'',102),
		('legacy-python-chart','legacy-python','chart.png','/tmp/chart.png','image/png',10,'',101)`)
	// Even strong message-level evidence cannot safely attribute individual old
	// artifacts. The mixed message deliberately lost its tool records, matching
	// old provider-error and moderation paths that retained only artifact blocks.
	exec(t, db, `INSERT INTO usage_stats(source_log_id,user_id,conversation_id,message_id,model_id,purpose,images_count,created_at)
		VALUES(1,'artist','conv','legacy-image','image-model','image',1,100)`)
	exec(t, db, `INSERT INTO billing_usage(id,user_id,conversation_id,message_id,model_id,purpose,images_count,created_at)
		VALUES
		('bu-mixed','artist','conv','legacy-mixed','image-model','image',1,101),
		('bu-python','artist','conv','legacy-python','image-model','image',1,102)`)

	ctx := context.Background()
	adminItems, err := ListAdminGeneratedImages(ctx, db, AdminGeneratedImageFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(adminItems) != 0 {
		t.Fatalf("admin gallery=%+v, want no unattributed legacy images", adminItems)
	}
	userItems, err := ListUserImageArtifactsForUser(ctx, db, "artist", 50, 0)
	if err != nil {
		t.Fatalf("user list: %v", err)
	}
	if len(userItems) != 0 {
		t.Fatalf("user gallery=%+v, want no unattributed legacy images", userItems)
	}
}
