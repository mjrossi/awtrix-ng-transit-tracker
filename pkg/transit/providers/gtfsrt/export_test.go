package gtfsrt

// WaitForSchedule blocks until a schedule download in flight, if any,
// has finished and recorded its outcome.
func (p *Provider) WaitForSchedule() {
	if p.schedule != nil {
		p.schedule.Wait()
	}
}
