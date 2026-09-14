// Package jointips 는 jointips.or.kr 회의실 예약의 HTTP 프로토콜을 구현한다.
// 셀렉터는 사이트가 jQuery 로 쓰는 것과 같은 표현을 쓴다(tr[data-wr8], td[data-time], .available).
package jointips

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Cell 은 조회 응답의 시간 셀 하나다. 30분 단위, 09:00~20:30 으로 24칸.
type Cell struct {
	Time      string `json:"time"` // "09:00"
	Idx       int    `json:"idx"`  // 0~23
	Available bool   `json:"available"`
	Title     string `json:"title"`    // selected 셀이면 회의명
	ShareID   string `json:"share_id"` // 같은 예약끼리 동일
}

// Row 는 회의실 하나(조회 응답의 <tr> 하나)다.
type Row struct {
	RoomID   string `json:"room_id"`  // data-wr8 → 폼의 wr_8
	Building string `json:"building"` // data-wr6 → wr_6
	Floor    string `json:"floor"`    // data-wr7 → wr_7
	Place    string `json:"place"`    // "현승빌딩 4층"
	Name     string `json:"name"`     // "회의실 A"
	Capacity string `json:"capacity"` // "4명"
	Cells    []Cell `json:"cells"`    // 센티널을 제외한 24칸
}

// Label 은 화면 표시용 이름이다.
func (r Row) Label() string {
	if r.Place == "" {
		return r.Name
	}
	return r.Place + " " + r.Name
}

// ParseRows 는 location_select_new 응답(tbody 내용 HTML 조각)을 파싱한다.
//
// 두 가지 함정을 처리한다(§3.4):
//   - 각 행 끝의 data-time="21:00" style="display:none" 센티널 셀은 기예약으로 세지 않는다.
//   - rowspan 때문에 두 번째 행부터는 "빌딩 층" 셀이 없다 → 같은 data-wr7 의 이전 행에서 물려받는다.
func ParseRows(fragment string) ([]Row, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<table><tbody>" + fragment + "</tbody></table>"))
	if err != nil {
		return nil, err
	}
	var rows []Row
	doc.Find("tr[data-wr8]").Each(func(_ int, tr *goquery.Selection) {
		row := Row{
			RoomID:   strings.TrimSpace(tr.AttrOr("data-wr8", "")),
			Building: strings.TrimSpace(tr.AttrOr("data-wr6", "")),
			Floor:    strings.TrimSpace(tr.AttrOr("data-wr7", "")),
		}

		// 시간 셀이 아닌 앞쪽 <td> 들 — 위치/이름/정원이 여기 있다.
		var lead []string
		tr.Find("td").Each(func(_ int, td *goquery.Selection) {
			if _, ok := td.Attr("data-time"); ok {
				return
			}
			lead = append(lead, strings.TrimSpace(td.Text()))
		})
		switch {
		case len(lead) >= 2 && strings.Contains(lead[0], "빌딩"):
			row.Place, row.Name = lead[0], lead[1]
		case len(lead) >= 1:
			row.Name = lead[0]
		}
		if len(lead) > 0 {
			row.Capacity = lead[len(lead)-1]
		}
		if row.Place == "" {
			// rowspan 으로 생략된 위치를 같은 층의 이전 행에서 물려받는다.
			for i := len(rows) - 1; i >= 0; i-- {
				if rows[i].Floor == row.Floor && rows[i].Place != "" {
					row.Place = rows[i].Place
					break
				}
			}
		}

		tr.Find("td[data-time]").Each(func(_ int, td *goquery.Selection) {
			// display:none 센티널(21:00)은 시간 격자에 넣지 않는다.
			if strings.Contains(strings.ReplaceAll(td.AttrOr("style", ""), " ", ""), "display:none") {
				return
			}
			class := td.AttrOr("class", "")
			cell := Cell{
				Time:      strings.TrimSpace(td.AttrOr("data-time", "")),
				Idx:       len(row.Cells),
				Available: hasClass(class, "available"),
				ShareID:   strings.TrimSpace(td.AttrOr("share_id", "")),
			}
			// available 셀의 title 은 그냥 시각 문자열이라 회의명이 아니다.
			if title := strings.TrimSpace(td.AttrOr("title", "")); !cell.Available && title != cell.Time {
				cell.Title = title
			}
			row.Cells = append(row.Cells, cell)
		})
		rows = append(rows, row)
	})
	return rows, nil
}

func hasClass(class, want string) bool {
	for _, f := range strings.Fields(class) {
		if f == want {
			return true
		}
	}
	return false
}
