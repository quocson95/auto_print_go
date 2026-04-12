package pdf

import "testing"

func TestGenerateFilenamePreservesVietnameseCharacters(t *testing.T) {
	g := &Generator{}

	got := g.generateFilename(`Nước Anh Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế"`)
	want := "Nước Anh Thiếu Đầu Tư Là Thứ Phá Hủy Nền Kinh Tế.pdf"

	if got != want {
		t.Fatalf("generateFilename() = %q, want %q", got, want)
	}
}
