// A Service's Pages (ADR 0010): the web UIs it offers. A Service has zero
// or more; the first is its main one, reached at <service>.local, and the
// others at <service>-<page>.local. The sidebar shows one entry for a
// Service with one Page, a nested list for several and nothing for none.
//
// A spec from before Pages describes its one page with webPort, webPath
// and webComponent; the Node fills `pages` in from those, and this reads
// them the same way for a Console talking to a Node mid-update.
import type { PageStatus, ServicePage, ServiceSpec, ServiceStatus } from "@/gen/maitre/v1/service_pb";
import type { FleetService } from "./useFleet";
import { domainLinks } from "./domain";

/** A Service's Pages, with one made from the web fields for a spec that has none. */
export function pagesOf(spec: ServiceSpec | undefined): ServicePage[] {
  if (!spec) return [];
  if (spec.pages.length > 0) return spec.pages;
  if (!spec.webPort && !spec.webHostPort) return [];
  return [{ $typeName: "maitre.v1.ServicePage", name: "web", component: spec.webComponent, port: spec.webPort, path: spec.webPath, exposure: "", publicHost: "", hostPort: spec.webHostPort, healthHttp: spec.healthHttp, label: "" }];
}

/** The host name a Page answers at, without the domain: the Service's own
 * name for its first Page, <service>-<page> for the others. It is also how
 * the Console keys the page's proxy and its native view. */
export function pageHost(spec: ServiceSpec, page: ServicePage): string {
  const pages = pagesOf(spec);
  return pages.length > 0 && pages[0].name === page.name ? spec.name : `${spec.name}-${page.name}`;
}

/** A Page by name; "" is the first. */
export function pageByName(spec: ServiceSpec | undefined, name: string): ServicePage | undefined {
  const pages = pagesOf(spec);
  return name ? pages.find((p) => p.name === name) : pages[0];
}

/** What the sidebar calls a Page: its label, else its name. */
export function pageLabel(page: ServicePage): string {
  return page.label || page.name;
}

/** Where a Page is reachable: its own level, else the Service's. */
export function pageExposure(spec: ServiceSpec, page: ServicePage): { exposure: string; publicHost: string } {
  return page.exposure ? { exposure: page.exposure, publicHost: page.publicHost } : { exposure: spec.exposure, publicHost: spec.publicHost };
}

/** A Page's status, by name; a Node from before Pages reports the
 * Service-wide url and health, which is its one Page's. */
export function pageStatusOf(status: ServiceStatus | undefined, page: ServicePage, first: boolean): PageStatus | undefined {
  if (!status) return undefined;
  const found = status.pages.find((p) => p.name === page.name);
  if (found) return found;
  if (!first) return undefined;
  return { $typeName: "maitre.v1.PageStatus", name: page.name, url: status.url, remoteUrl: status.remoteUrl, remoteState: status.remoteState, healthy: status.healthy, homeState: "" };
}

/** The address a Page is shown and opened at (decision 329): its address
 * at the user's domain when the Fleet asks for those and it answers from
 * home as well as away, so one link works everywhere; else its .local
 * one. A Page that is not at the domain has only the .local one. */
export function pageLink(spec: ServiceSpec, page: ServicePage, st: PageStatus | undefined, preferDomain = domainLinks()): string {
  if (!st) return "";
  const atDomain = pageExposure(spec, page).exposure === "direct" && st.remoteState === "up" && st.homeState === "up" && !!st.remoteUrl;
  return preferDomain && atDomain ? st.remoteUrl : st.url;
}

/** A Page as the Console shows it: the Service it belongs to, the Page, its
 * host name and what its status says. */
export interface PageView {
  service: FleetService;
  page: ServicePage;
  /** Its host name, which keys its proxy, its view and its frame. */
  host: string;
  /** Its address on the home network: http://<host>.local<path>. */
  url: string;
  /** The address shown and opened in a browser: url, or its address at
   * the user's domain (decision 329). */
  link: string;
  remoteUrl: string;
  label: string;
  first: boolean;
}

/** Every Page of a Service, in the spec's order. */
export function pageViews(service: FleetService): PageView[] {
  const pages = pagesOf(service.spec);
  return pages.map((page, i) => {
    const st = pageStatusOf(service.status, page, i === 0);
    return {
      service,
      page,
      host: pageHost(service.spec, page),
      url: st?.url ?? "",
      link: pageLink(service.spec, page, st),
      remoteUrl: st?.remoteUrl ?? "",
      label: pageLabel(page),
      first: i === 0,
    };
  });
}

/** One Page of a Service by name, for a route; the first when unnamed. */
export function pageViewOf(service: FleetService, name: string): PageView | undefined {
  const views = pageViews(service);
  return name ? views.find((v) => v.page.name === name) : views[0];
}

/** The Page a Service's own entry in the sidebar opens: its only Page, or
 * its first when it has several. Undefined for a Service with none. */
export function mainPage(service: FleetService): PageView | undefined {
  return pageViews(service)[0];
}

/** Where the Pages at the user's domain stand from home (decision 329):
 * those whose domain address answers there, those it does not with why,
 * and those not checked yet (not up from the internet, or a Node from
 * before the check). */
export interface DomainLinkCheck {
  home: string[];
  away: { host: string; why: string }[];
  unchecked: string[];
}

export function domainLinkCheck(services: FleetService[]): DomainLinkCheck {
  const out: DomainLinkCheck = { home: [], away: [], unchecked: [] };
  for (const s of services) {
    pagesOf(s.spec).forEach((page, i) => {
      if (pageExposure(s.spec, page).exposure !== "direct") return;
      const host = pageHost(s.spec, page);
      const st = pageStatusOf(s.status, page, i === 0);
      if (st?.remoteState !== "up" || !st.homeState) out.unchecked.push(host);
      else if (st.homeState === "up") out.home.push(host);
      else out.away.push({ host, why: st.homeState });
    });
  }
  return out;
}
