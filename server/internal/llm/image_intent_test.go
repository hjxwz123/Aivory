package llm

import "testing"

func TestFallbackDirectImageTurnPlanUsesIntentAndExplicitSource(t *testing.T) {
	tests := []struct {
		name          string
		prompt        string
		currentImages int
		hasPrevious   bool
		wantAction    string
		wantBase      string
		wantBaseIndex int
	}{
		{
			name:          "new generation ignores available images",
			prompt:        "参考这些图片生成一张全新的产品海报",
			currentImages: 2, hasPrevious: true,
			wantAction: "generate", wantBase: "none",
		},
		{
			name:          "screenshot scenario edits prior result",
			prompt:        "右侧换成网站页面，右下角硬件参考图2，其他不变",
			currentImages: 2, hasPrevious: true,
			wantAction: "edit", wantBase: "previous_generation",
		},
		{
			name:          "current second attachment is authoritative",
			prompt:        "把本轮上传的第2张图背景改成白色，第一张作为参考",
			currentImages: 2, hasPrevious: true,
			wantAction: "edit", wantBase: "current_attachment", wantBaseIndex: 2,
		},
		{
			name:          "explicit previous edit without current uploads",
			prompt:        "只修改上一张图的标题，其他不变",
			currentImages: 0, hasPrevious: true,
			wantAction: "edit", wantBase: "previous_generation",
		},
		{
			name:          "ambiguous follow up remains generation",
			prompt:        "做一个未来科技主题",
			currentImages: 0, hasPrevious: true,
			wantAction: "generate", wantBase: "none",
		},
		{
			name:          "ambiguous edit base does not default to previous",
			prompt:        "修改这张图的背景",
			currentImages: 2, hasPrevious: true,
			wantAction: "edit", wantBase: "none",
		},
		{
			name:          "single available upload is the only possible edit base",
			prompt:        "修改这张图的背景",
			currentImages: 1, hasPrevious: false,
			wantAction: "edit", wantBase: "current_attachment", wantBaseIndex: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fallbackDirectImageTurnPlan(tt.prompt, "", tt.currentImages, tt.hasPrevious)
			if got.Action != tt.wantAction || got.BaseImage != tt.wantBase || got.BaseImageIndex != tt.wantBaseIndex {
				t.Fatalf("plan = %+v, want action=%s base=%s index=%d", got, tt.wantAction, tt.wantBase, tt.wantBaseIndex)
			}
		})
	}
}

func TestNormalizeDirectImageTurnPlanRejectsUnavailableSources(t *testing.T) {
	fallback := directImageTurnPlan{Action: "generate", BaseImage: "none", Prompt: "fallback"}
	tests := []struct {
		name          string
		candidate     directImageTurnPlan
		currentImages int
		hasPrevious   bool
		want          directImageTurnPlan
	}{
		{
			name:      "missing previous",
			candidate: directImageTurnPlan{Action: "edit", BaseImage: "previous_generation", Prompt: "optimized"},
			want:      directImageTurnPlan{Action: "edit", BaseImage: "none", Prompt: "optimized"},
		},
		{
			name:          "current index out of range",
			candidate:     directImageTurnPlan{Action: "edit", BaseImage: "current_attachment", BaseImageIndex: 3, Prompt: "optimized"},
			currentImages: 2,
			want:          directImageTurnPlan{Action: "edit", BaseImage: "none", Prompt: "optimized"},
		},
		{
			name:          "valid selected current attachment",
			candidate:     directImageTurnPlan{Action: "edit", BaseImage: "current_attachment", BaseImageIndex: 2, Prompt: "optimized"},
			currentImages: 2, hasPrevious: true,
			want: directImageTurnPlan{Action: "edit", BaseImage: "current_attachment", BaseImageIndex: 2, Prompt: "optimized"},
		},
		{
			name:          "generation clears accidental source",
			candidate:     directImageTurnPlan{Action: "generate", BaseImage: "previous_generation", BaseImageIndex: 1, Prompt: "optimized"},
			currentImages: 1, hasPrevious: true,
			want: directImageTurnPlan{Action: "generate", BaseImage: "none", Prompt: "optimized"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeDirectImageTurnPlan(tt.candidate, fallback, tt.currentImages, tt.hasPrevious, true, "")
			if got != tt.want {
				t.Fatalf("normalized plan = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestNormalizeDirectImageTurnPlanCannotOverrideExplicitOperation(t *testing.T) {
	editFallback := directImageTurnPlan{Action: "edit", BaseImage: "previous_generation", Prompt: "literal edit"}
	if got := normalizeDirectImageTurnPlan(
		directImageTurnPlan{Action: "generate", BaseImage: "none", Prompt: "new image"},
		editFallback, 0, true, true, "edit",
	); got != editFallback {
		t.Fatalf("explicit edit was overridden by generation: %+v", got)
	}

	generateFallback := directImageTurnPlan{Action: "generate", BaseImage: "none", Prompt: "new poster"}
	if got := normalizeDirectImageTurnPlan(
		directImageTurnPlan{Action: "edit", BaseImage: "current_attachment", BaseImageIndex: 1, Prompt: "edit upload"},
		generateFallback, 1, false, true, "generate",
	); got != generateFallback {
		t.Fatalf("explicit generation was overridden by edit: %+v", got)
	}
}
