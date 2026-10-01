package jointips

// Candidate 는 폴백 탐색이 찾아낸 빈 자리다.
type Candidate struct {
	Row      Row
	StartMin int
	Slots    int
}

// Start 는 후보의 시작 시각을 "HH:MM" 으로 돌려준다.
func (c Candidate) Start() string { return formatHHMM(c.StartMin) }

// Match 는 주어진 행들에서 조건에 맞는 가장 좋은 빈 자리를 찾는다.
//
// 후보 조건: 시작 셀이 available 이고 targetMin ~ targetMin+windowMin 사이에서 시작하며,
// 거기서부터 연속 available 슬롯이 minSlots 이상. 길이는 maxSlots 까지만 취한다.
// 우선순위: ① 시작이 이른 것 → ② 동률이면 긴 것.
func Match(rows []Row, targetMin, windowMin, minSlots, maxSlots int) *Candidate {
	var best *Candidate
	for _, row := range rows {
		if !row.Bookable {
			continue
		}
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

// MatchWithPreference 는 3단계 탐색 순서를 구현한다.
//  1. bldgCd + preferFloor 에 해당하는 행
//  2. bldgCd 의 모든 층
//  3. (bldgCd 가 비어 있으면) 전체 행
func MatchWithPreference(rows []Row, bldgCd, preferFloor string, targetMin, windowMin, minSlots, maxSlots int) *Candidate {
	if bldgCd != "" && preferFloor != "" {
		if c := Match(filter(rows, bldgCd, preferFloor), targetMin, windowMin, minSlots, maxSlots); c != nil {
			return c
		}
	}
	if bldgCd != "" {
		if c := Match(filter(rows, bldgCd, ""), targetMin, windowMin, minSlots, maxSlots); c != nil {
			return c
		}
	}
	return Match(rows, targetMin, windowMin, minSlots, maxSlots)
}

func filter(rows []Row, bldgCd, floor string) []Row {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		if bldgCd != "" && r.BldgCd != bldgCd {
			continue
		}
		if floor != "" && r.FloorNo != floor {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Occupied 는 지정 구간이 모두 이미 차 있는지 본다.
//
// 예전에는 셀 title 의 회의명까지 대조했지만, 슬롯 API 는 누가 잡았는지 알려주지 않는다.
// 그래서 이것만으로는 '내가 잡았다'를 증명하지 못한다 — 예약 성공의 최종 근거는
// 내 예약 목록(MyReservations)이고, 이 함수는 그 보조다.
func (r Row) Occupied(start string, slots int) bool {
	startMin, err := parseHHMM(start)
	if err != nil {
		return false
	}
	unit := r.SlotUnit
	if unit == 0 {
		unit = slotMinutes
	}
	hit := 0
	for _, c := range r.Cells {
		m, err := parseHHMM(c.Time)
		if err != nil || m < startMin || m >= startMin+slots*unit {
			continue
		}
		if c.Status == SlotUnavailable {
			hit++
		}
	}
	return hit == slots
}
