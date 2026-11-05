package services

import (
	"fmt"
	"regexp"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Overrides (ADR 0010) are the changes a person made to a container's
// [Container] options in Setup, kept on the Service's record and applied
// after the bundle's template is rendered: the bundle stays as the Catalog
// signed it, a Catalog update keeps the person's changes, and what an
// override adds goes through the grant check like a template line.

// multiValued are [Container] keys Quadlet reads as lists, where setting
// a value adds a line rather than replacing one.
var multiValued = map[string]bool{
	"AddCapability": true, "AddDevice": true, "AddHost": true, "Annotation": true, "DNS": true, "DNSOption": true,
	"DNSSearch": true, "DropCapability": true, "Environment": true, "EnvironmentFile": true, "ExposeHostPort": true,
	"GroupAdd": true, "Label": true, "Mask": true, "Mount": true, "Network": true, "PodmanArgs": true,
	"PublishPort": true, "Secret": true, "Sysctl": true, "Tmpfs": true, "Ulimit": true, "Unmask": true, "Volume": true,
}

var overrideKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,40}$`)

// validateOverrides refuses an override that could not land as one line
// of a unit, or names a component the Service lacks.
func validateOverrides(spec *maitrev1.ServiceSpec) error {
	comps := map[string]bool{}
	for _, c := range spec.GetComponents() {
		comps[c.GetName()] = true
	}
	for _, o := range spec.GetOverrides() {
		switch {
		case !overrideKeyRe.MatchString(o.GetKey()):
			return fmt.Errorf("option %q: a [Container] key is letters and digits", o.GetKey())
		case strings.ContainsAny(o.GetValue(), "\n\r\x00"):
			return fmt.Errorf("option %s has a line break", o.GetKey())
		case !o.GetRemove() && strings.TrimSpace(o.GetValue()) == "":
			return fmt.Errorf("option %s has no value", o.GetKey())
		case isMulti(spec) && !comps[o.GetComponent()]:
			return fmt.Errorf("option %s: %q is not a component of %s", o.GetKey(), o.GetComponent(), spec.GetName())
		case !isMulti(spec) && o.GetComponent() != "":
			return fmt.Errorf("option %s names a component, and %s has none", o.GetKey(), spec.GetName())
		}
	}
	return nil
}

// overridesFor are the overrides of one component.
func overridesFor(spec *maitrev1.ServiceSpec, component string) []*maitrev1.UnitOverride {
	var out []*maitrev1.UnitOverride
	for _, o := range spec.GetOverrides() {
		if o.GetComponent() == component {
			out = append(out, o)
		}
	}
	return out
}

// applyOverrides applies overrides to the [Container] lines of a unit.
// Setting a single-valued key replaces its line; setting a list key adds
// a line unless that exact one is there; removing drops every Key=value
// line, or every Key= line when value is empty. Lines added or changed
// are marked as overrides.
func applyOverrides(u unit, overrides []*maitrev1.UnitOverride, removeOnly bool) unit {
	for _, o := range overrides {
		key := o.GetKey()
		if o.GetRemove() {
			u = u.filterContainer(func(l line) bool {
				k, v, ok := keyValue(l.text)
				if !ok || k != key {
					return true
				}
				return o.GetValue() != "" && strings.TrimSpace(v) != strings.TrimSpace(o.GetValue())
			})
			continue
		}
		if removeOnly {
			continue
		}
		text := key + "=" + o.GetValue()
		present := false
		for _, l := range u {
			if k, v, ok := keyValue(l.text); ok && k == key && strings.TrimSpace(v) == strings.TrimSpace(o.GetValue()) {
				present = true
			}
		}
		if present {
			continue
		}
		if multiValued[key] {
			u = u.appendContainer(line{text, srcOverride})
			continue
		}
		replaced := false
		u = u.mapContainer(func(l line) (line, bool) {
			if k, _, ok := keyValue(l.text); ok && k == key {
				if replaced {
					return l, false
				}
				replaced = true
				return line{text, srcOverride}, true
			}
			return l, true
		})
		if !replaced {
			u = u.appendContainer(line{text, srcOverride})
		}
	}
	return u
}

// keyValue splits a unit line into key and value.
func keyValue(text string) (key, value string, ok bool) {
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "[") {
		return "", "", false
	}
	k, v, found := strings.Cut(t, "=")
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(k), v, true
}
