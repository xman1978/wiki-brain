package index

import (
	"strings"
	"testing"
)

func TestITDictSegmentation(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"数据结构与算法", []string{"数据结构"}},
		{"配置文件加载", []string{"配置文件", "加载"}},
		{"知识单元提取", []string{"知识单元"}},
		{"股票涨停板分析", []string{"涨停板"}},
		{"融资融券交易", []string{"融资融券"}},
	}

	for _, tt := range tests {
		segments := seg.Segment([]byte(tt.input))
		var words []string
		for _, s := range segments {
			w := s.Token().Text()
			if strings.TrimSpace(w) != "" {
				words = append(words, w)
			}
		}

		for _, exp := range tt.expected {
			found := false
			for _, w := range words {
				if w == exp {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("input=%q: expected token %q not found in %v", tt.input, exp, words)
			}
		}
	}
}
