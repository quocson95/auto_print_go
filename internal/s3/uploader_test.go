package s3

import "testing"

func TestLegacyKeyForEntryMatchesOldASCIIKey(t *testing.T) {
	entry := IndexEntry{
		Key:   "pdfs/[PC] Nước Anh Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế.pdf",
		Title: "Nước Anh Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế",
	}

	got := legacyKeyForEntry(entry)
	want := "pdfs/[PC] Nc Anh Thiu u T L Th Ph Hy Nn Kinh T.pdf"

	if got != want {
		t.Fatalf("legacyKeyForEntry() = %q, want %q", got, want)
	}
}

func TestLegacyKeyForEntryReturnsEmptyWithoutVariantPrefix(t *testing.T) {
	entry := IndexEntry{
		Key:   "pdfs/article.pdf",
		Title: "Some title",
	}

	if got := legacyKeyForEntry(entry); got != "" {
		t.Fatalf("legacyKeyForEntry() = %q, want empty", got)
	}
}

func TestMigrateIndexEntriesAppliesOverrideAndRenamesKey(t *testing.T) {
	entries := []IndexEntry{
		{
			Key:          "pdfs/[PC] Nc Anh Thiu u T L Th Ph Hy Nn Kinh T.pdf",
			LastModified: "2026-04-12T10:00:00.000000Z",
			Size:         123,
		},
	}

	updated, result, copies := migrateIndexEntries(entries, map[string]string{
		"Nc Anh Thiu u T L Th Ph Hy Nn Kinh T": "Nước Anh Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế",
	})

	if len(updated) != 1 {
		t.Fatalf("len(updated) = %d, want 1", len(updated))
	}
	if updated[0].Key != "pdfs/[PC] Nước Anh Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế.pdf" {
		t.Fatalf("unexpected migrated key: %q", updated[0].Key)
	}
	if updated[0].Title != "Nước Anh Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế" {
		t.Fatalf("unexpected title: %q", updated[0].Title)
	}
	if updated[0].Name != updated[0].Title {
		t.Fatalf("expected name to match title, got %q vs %q", updated[0].Name, updated[0].Title)
	}
	if result.RenamedKeys != 1 || result.UpdatedTitles != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(copies) != 1 || copies[0].FromKey != entries[0].Key || copies[0].ToKey != updated[0].Key {
		t.Fatalf("unexpected copies: %#v", copies)
	}
}

func TestMigrateIndexEntriesRemovesDuplicateWhenOverrideMatchesExistingNewKey(t *testing.T) {
	entries := []IndexEntry{
		{
			Key:          "pdfs/[PC] Nc Anh Thiu u T L Th Ph Hy Nn Kinh T.pdf",
			LastModified: "2026-04-12T10:00:00.000000Z",
			Size:         111,
		},
		{
			Key:          "pdfs/[PC] Nước Anh Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế.pdf",
			Title:        "Nước Anh Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế",
			Name:         "Nước Anh Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế",
			LastModified: "2026-04-12T11:00:00.000000Z",
			Size:         222,
		},
	}

	updated, result, _ := migrateIndexEntries(entries, map[string]string{
		"Nc Anh Thiu u T L Th Ph Hy Nn Kinh T": "Nước Anh Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế",
	})

	if len(updated) != 1 {
		t.Fatalf("len(updated) = %d, want 1", len(updated))
	}
	if result.RemovedDuplicates != 1 {
		t.Fatalf("RemovedDuplicates = %d, want 1", result.RemovedDuplicates)
	}
	if updated[0].Key != "pdfs/[PC] Nước Anh Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế.pdf" {
		t.Fatalf("unexpected remaining key: %q", updated[0].Key)
	}
}

func TestMigrateIndexEntriesLeavesNonPDFAssetsUntouched(t *testing.T) {
	entries := []IndexEntry{
		{
			Key:          "pdfs/thumbnails/NVIDIA-GTC-2026-Khi-AI-Lên-Hệ-Sinh-Thái.webp",
			LastModified: "2026-04-12T10:00:00.000000Z",
			Size:         321,
		},
	}

	updated, result, copies := migrateIndexEntries(entries, map[string]string{
		"NVIDIA-GTC-2026-Khi-AI-Lên-Hệ-Sinh-Thái.webp": "Some other title",
	})

	if len(updated) != 1 {
		t.Fatalf("len(updated) = %d, want 1", len(updated))
	}
	if updated[0].Key != entries[0].Key {
		t.Fatalf("non-PDF key changed: %q", updated[0].Key)
	}
	if updated[0].Title != "" || updated[0].Name != "" {
		t.Fatalf("non-PDF metadata unexpectedly changed: %#v", updated[0])
	}
	if result.RenamedKeys != 0 || result.UpdatedTitles != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(copies) != 0 {
		t.Fatalf("unexpected copies: %#v", copies)
	}
}

func TestExportedMigrateIndexEntriesKeepsExistingKeysForLocalFileMode(t *testing.T) {
	entries := []IndexEntry{
		{
			Key:          "pdfs/[PC] Nc Anh Thiu u T L Th Ph Hy Nn Kinh T.pdf",
			LastModified: "2026-04-12T10:00:00.000000Z",
			Size:         123,
		},
	}

	updated, result := MigrateIndexEntries(entries, map[string]string{
		"Nc Anh Thiu u T L Th Ph Hy Nn Kinh T": "Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế",
	})

	if len(updated) != 1 {
		t.Fatalf("len(updated) = %d, want 1", len(updated))
	}
	if updated[0].Key != entries[0].Key {
		t.Fatalf("local migration changed key: %q", updated[0].Key)
	}
	if updated[0].Title != "Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế" {
		t.Fatalf("unexpected title: %q", updated[0].Title)
	}
	if updated[0].Name != updated[0].Title {
		t.Fatalf("expected name to match title, got %q vs %q", updated[0].Name, updated[0].Title)
	}
	if result.RenamedKeys != 0 {
		t.Fatalf("local migration should not rename keys: %+v", result)
	}
}
