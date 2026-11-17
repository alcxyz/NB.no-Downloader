package ocr

import (
	"testing"
)

func TestParseTSV(t *testing.T) {
	tsv := "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
		"1\t1\t0\t0\t0\t0\t0\t0\t1000\t1500\t-1\t\n" +
		"2\t1\t1\t0\t0\t0\t100\t100\t800\t300\t-1\t\n" + // block without paragraphs: a picture
		"2\t1\t2\t0\t0\t0\t100\t500\t800\t100\t-1\t\n" +
		"3\t1\t2\t1\t0\t0\t100\t500\t800\t100\t-1\t\n" +
		"4\t1\t2\t1\t1\t0\t100\t500\t800\t40\t-1\t\n" +
		"5\t1\t2\t1\t1\t1\t100\t500\t200\t40\t96.5\tHei\n" +
		"5\t1\t2\t1\t1\t2\t320\t500\t200\t40\t91\tverden\n"
	pg, err := ParseTSV(tsv)
	if err != nil {
		t.Fatal(err)
	}
	if pg.Width != 1000 || pg.Height != 1500 || len(pg.Blocks) != 2 {
		t.Fatalf("page %gx%g with %d blocks", pg.Width, pg.Height, len(pg.Blocks))
	}
	if !pg.Blocks[0].Image || pg.Blocks[1].Image {
		t.Error("first block should be a picture, second text")
	}
	words := pg.Blocks[1].Lines[0].Words
	if len(words) != 2 || words[1].Text != "verden" || words[0].Conf != 0.965 {
		t.Errorf("words = %+v", words)
	}
}
