package jointips

// Candidate 는 폴백 탐색이 찾아낸 빈 자리다.
type Candidate struct {
	Row      Row
	StartMin int
	Slots    int
}

// Start 는 후보의 시작 시각을 "HH:MM" 으로 돌려준다.
func (c Candidate) Start() string { return formatHHMM(c.StartMin) }

// Match 는 주어진 행들에서 조건에 맞는 가장 좋은 빈 자리를 찾는다(§5.2).
//
// 후보 조건: 시작 셀이 available 이고 targetMin ~ targetMin+windowMin 사이에서 시작하며,
// 거기서부터 연속 available 슬롯이 minSlots 이상. 길이는 maxSlots 까지만 취한다.
// 우선순위: ① 시작이 이른 것 → ② 동률이면 긴 것.
func Match(rows []Row, targetMin, windowMin, minSlots, maxSlots int) *Candidate {
	var best *Candidate
	for _, row := range rows {
		for i, c := range row.Cells {
			if !c.Available {
				continue
			}
			startMin, err := parseHHMM(c.Time)
			if err != nil {
				continue
			}
			if startMin < targetMin {
				continue
			}
			if startMin > targetMin+windowMin {
				break // 셀이 시간 오름차순이므로 더 볼 필요 없다
			}
			run := 0
			for j := i; j < len(row.Cells) && run < maxSlots; j++ {
				if !row.Cells[j].Available {
					break
				}
				run++
			}
			if run < minSlots {
				continue
			}
			cand := Candidate{Row: row, StartMin: startMin, Slots: run}
			if best == nil || cand.StartMin < best.StartMin ||
				(cand.StartMin == best.StartMin && cand.Slots > best.Slots) {
				c := cand
				best = &c
			}
		}
	}
	return best
}

// MatchWithPreference 는 §5.2 의 3단계 탐색 순서를 구현한다.
//  1. building + preferFloor 에 해당하는 행
//  2. building 의 모든 층
//  3. (building 이 비어 있으면) 전체 행
func MatchWithPreference(rows []Row, building, preferFloor string, targetMin, windowMin, minSlots, maxSlots int) *Candidate {
	if building != "" && preferFloor != "" {
		if c := Match(filter(rows, building, preferFloor), targetMin, windowMin, minSlots, maxSlots); c != nil {
			return c
		}
	}
	if building != "" {
		if c := Match(filter(rows, building, ""), targetMin, windowMin, minSlots, maxSlots); c != nil {
			return c
		}
	}
	return Match(rows, targetMin, windowMin, minSlots, maxSlots)
}

func filter(rows []Row, building, floor string) []Row {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		if building != "" && r.Building != building {
			continue
		}
		if floor != "" && r.Floor != floor {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Occupied 는 지정 구간이 모두 이미 예약(selected)되었고 title 이 subject 를 포함하는지 본다.
// 예약 성공의 최종 근거(§3.6 2차 판정)로 쓴다.
func (r Row) Occupied(start string, slots int, subject string) bool {
	startMin, err := parseHHMM(start)
	if err != nil {
		return false
	}
	hit := 0
	for _, c := range r.Cells {
		m, err := parseHHMM(c.Time)
		if err != nil || m < startMin || m >= startMin+slots*slotMinutes {
			continue
		}
		if c.Available {
			return false
		}
		if subject == "" || containsFold(c.Title, subject) {
			hit++
		}
	}
	return hit == slots
}

func containsFold(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	return len(haystack) >= len(needle) && indexFold(haystack, needle) >= 0
}

func indexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
