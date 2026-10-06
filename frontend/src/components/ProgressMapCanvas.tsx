// Ambient progress feedback for a lookup: the searched area, with neighbouring
// buildings revealed as the Boliga work behind them actually completes. It
// reads the per-street signal the relay reports, so nothing here is on a timer.
//
// The map is decoration and degrades in two steps. Without WebGL it renders
// nothing at all and gives its space back. With WebGL but no tiles, the dots
// still appear over a blank basemap, which is worth more than nothing.

import { useEffect, useMemo, useRef, useState } from "react";
import type { FeatureCollection } from "geojson";
import type { GeoJSONSource, LngLatBoundsLike, Map as MapLibreMap } from "maplibre-gl";
import maplibregl from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
import type { MapProgress } from "@/lib/types";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { streetKey } from "@/lib/boliga";

// Positron is grayscale at the source. Desaturating a colour style with a CSS
// filter would have washed out the orange dots too: they render into the same
// WebGL canvas as the basemap.
const STYLE_URL = "https://tiles.openfreemap.org/styles/positron";

const ORANGE = "#f97316";
const DOT_RADIUS = 2.6;
const DOT_OPACITY = 0.85;
// Entering dots start larger and transparent, then settle. Restrained on
// purpose — fifty streets arriving over three minutes should feel like a city
// filling in, not like a notification.
const ENTER_RADIUS = 7;
// Must stay under SETTLE_MS in useProgress.ts: that is how long the finished
// map is held before the results replace it, so a longer entrance would have
// the closing reveal cut off halfway through.
const ENTER_MS = 550;

const SETTLED = "hjem-settled";
const ENTERING = "hjem-entering";
const MISSED = "hjem-missed";
const SUBJECT = "hjem-subject";

type Points = [number, number][];

function pointCollection(points: Points): FeatureCollection {
  return {
    type: "FeatureCollection",
    features: points.map(([lat, lon]) => ({
      type: "Feature",
      properties: {},
      geometry: { type: "Point", coordinates: [lon, lat] },
    })),
  };
}

// The bounding box of the search circle. Longitude degrees shorten towards the
// poles, so at Danish latitudes an east-west degree covers barely half what a
// north-south one does — one delta for both axes would frame a box a third too
// narrow and crop the dots on either side.
function radiusBounds(lat: number, lon: number, radiusM: number): LngLatBoundsLike {
  const dLat = radiusM / 111_320;
  const dLon = radiusM / (111_320 * Math.cos((lat * Math.PI) / 180));
  return [
    [lon - dLon, lat - dLat],
    [lon + dLon, lat + dLat],
  ];
}

export interface ProgressMapProps {
  map: MapProgress | null;
}

export function ProgressMapCanvas({ map }: ProgressMapProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MapLibreMap | null>(null);
  const readyRef = useRef(false);

  // Which streets have already been drawn. Reveals are cumulative, so without
  // this a street arriving would re-animate everything already on screen.
  const drawnRef = useRef(new Set<string>());
  const settledRef = useRef<Points>([]);
  const enteringRef = useRef<Points>([]);
  const missedRef = useRef<Points>([]);

  // maplibre needs a sized container to construct into, so the box has to exist
  // before we know whether it will work. This collapses it again if it doesn't.
  const [broken, setBroken] = useState(false);
  const reduceMotion = useMediaQuery("(prefers-reduced-motion: reduce)");

  const plan = map?.plan ?? null;

  // Only the sources named actually get rewritten: setData re-indexes the whole
  // collection, and the settled bucket grows to every point in the plan.
  //
  // Nothing is allowed out of here. This is called from an effect, in a tree
  // with no error boundary, so an exception escaping would unmount the
  // dashboard the user waited minutes for. maplibre is third-party code driven
  // by live network and GPU state; the map is decoration and stops updating
  // rather than taking the results down with it.
  function draw(m: MapLibreMap, ...changed: string[]) {
    try {
      const points: Record<string, Points> = {
        [SETTLED]: settledRef.current,
        [ENTERING]: enteringRef.current,
        [MISSED]: missedRef.current,
      };
      for (const id of changed) {
        (m.getSource(id) as GeoJSONSource | undefined)?.setData(
          pointCollection(points[id])
        );
      }

      if (!changed.includes(ENTERING) || !enteringRef.current.length) return;
      if (reduceMotion) return;

      // Paint transitions do not fire for features that are merely added, so
      // the entrance is animated on the layer instead: snap the whole entering
      // batch to its start values, then transition the layer to the resting
      // ones.
      setEnterPaint(m, 0, ENTER_RADIUS, 0);
      requestAnimationFrame(() => {
        if (mapRef.current === m) setEnterPaint(m, ENTER_MS, DOT_RADIUS, DOT_OPACITY);
      });
    } catch (err) {
      console.warn("[hjem] Progress map:", err);
    }
  }

  useEffect(() => {
    if (!plan || !containerRef.current) return;

    let m: MapLibreMap;
    try {
      m = new maplibregl.Map({
        container: containerRef.current,
        style: STYLE_URL,
        bounds: radiusBounds(plan.lat, plan.lon, plan.radius_m),
        fitBoundsOptions: { padding: 24 },
        // Ambient feedback, not a map the user is meant to drive. This also
        // stops the element swallowing scrolls on mobile.
        interactive: false,
        attributionControl: { compact: true },
      });
    } catch (err) {
      // No WebGL, most likely. The progress bar above still works.
      console.warn("[hjem] Progress map unavailable:", err);
      setBroken(true);
      return;
    }

    mapRef.current = m;
    m.on("error", (e) => console.warn("[hjem] Progress map:", e.error));
    m.on("load", () => {
      for (const id of [MISSED, SETTLED, ENTERING, SUBJECT]) {
        m.addSource(id, { type: "geojson", data: pointCollection([]) });
      }

      // Streets Boliga refused: outlined rather than filled, so a failed fetch
      // never reads as data that arrived.
      m.addLayer({
        id: MISSED,
        type: "circle",
        source: MISSED,
        paint: {
          "circle-radius": DOT_RADIUS,
          "circle-opacity": 0,
          "circle-stroke-width": 1,
          "circle-stroke-color": ORANGE,
          "circle-stroke-opacity": 0.35,
        },
      });
      for (const id of [SETTLED, ENTERING]) {
        m.addLayer({
          id,
          type: "circle",
          source: id,
          paint: {
            "circle-radius": DOT_RADIUS,
            "circle-color": ORANGE,
            "circle-opacity": DOT_OPACITY,
          },
        });
      }
      // The searched property, added last so neighbours cannot cover it.
      m.addLayer({
        id: SUBJECT,
        type: "circle",
        source: SUBJECT,
        paint: {
          "circle-radius": 7,
          "circle-opacity": 0,
          "circle-stroke-width": 2.5,
          "circle-stroke-color": ORANGE,
        },
      });
      (m.getSource(SUBJECT) as GeoJSONSource).setData(
        pointCollection([[plan.lat, plan.lon]])
      );

      readyRef.current = true;
      // Reveals recorded before the style finished loading are still sitting
      // in the refs, so draw them now rather than waiting for the next poll.
      draw(m, SETTLED, ENTERING, MISSED);
    });

    return () => {
      readyRef.current = false;
      mapRef.current = null;
      drawnRef.current = new Set();
      settledRef.current = [];
      enteringRef.current = [];
      missedRef.current = [];
      m.remove();
    };
  }, [plan]);

  const byStreet = useMemo(
    () => new Map((plan?.streets ?? []).map((s) => [streetKey(s.task), s.points])),
    [plan]
  );

  useEffect(() => {
    if (!map) return;

    // Drawing a street is one-way, which is what makes the cumulative buckets
    // below safe to rebuild from the full done/failed sets each time.
    const take = (keys: Iterable<string>, into: Points) => {
      let added = false;
      for (const key of keys) {
        if (drawnRef.current.has(key)) continue;
        drawnRef.current.add(key);
        const points = byStreet.get(key);
        if (!points?.length) continue;
        into.push(...points);
        added = true;
      }
      return added;
    };

    // Flushed first: whatever was mid-animation has had its moment, and
    // leaving it in the entering source would restart its fade.
    settledRef.current.push(...enteringRef.current);
    enteringRef.current = [];

    const changed = new Set([SETTLED]);
    if (take(map.done, enteringRef.current)) changed.add(ENTERING);
    if (take(map.failed, missedRef.current)) changed.add(MISSED);

    if (map.settled) {
      // The server fetches whatever the browser could not, so once the lookup
      // is over the streets we never heard about were covered after all.
      if (take(byStreet.keys(), enteringRef.current)) changed.add(ENTERING);
      // Including the ones the browser was refused, so they stop being drawn
      // as failures.
      if (missedRef.current.length) {
        enteringRef.current.push(...missedRef.current);
        missedRef.current = [];
        changed.add(ENTERING).add(MISSED);
      }
    }

    if (changed.size === 1) return;
    if (mapRef.current && readyRef.current) draw(mapRef.current, ...changed);
  }, [map, byStreet]);

  if (!plan || broken) return null;

  return (
    <div className="relative h-[320px] overflow-hidden sm:h-[420px]">
      <div ref={containerRef} className="size-full" />
      {/* Feathers the map into the page rather than framing it as a card. */}
      <div
        className="pointer-events-none absolute inset-0"
        style={{
          background:
            "radial-gradient(ellipse at center, transparent 25%, var(--color-background) 78%)",
        }}
      />
    </div>
  );
}

function setEnterPaint(m: MapLibreMap, duration: number, radius: number, opacity: number) {
  m.setPaintProperty(ENTERING, "circle-radius-transition", { duration });
  m.setPaintProperty(ENTERING, "circle-opacity-transition", { duration });
  m.setPaintProperty(ENTERING, "circle-radius", radius);
  m.setPaintProperty(ENTERING, "circle-opacity", opacity);
}
