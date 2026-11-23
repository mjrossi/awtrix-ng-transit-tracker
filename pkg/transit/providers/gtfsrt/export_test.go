package gtfsrt

// WaitForSchedule blocks until a schedule download in flight, if any,
// has finished and recorded its outcome.
func (p *Provider) WaitForSchedule() {
	p.mu.Lock()
	loading := p.staticLoading
	p.mu.Unlock()
	if loading != nil {
		<-loading
	}
}
