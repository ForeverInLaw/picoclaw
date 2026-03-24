package agent

type toolAllowlist map[string]struct{}

func newToolAllowlist(names []string) toolAllowlist {
	if len(names) == 0 {
		return nil
	}

	allowed := make(toolAllowlist, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		allowed[name] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil
	}
	return allowed
}

func (a toolAllowlist) Allows(name string) bool {
	if len(a) == 0 {
		return true
	}
	_, ok := a[name]
	return ok
}
