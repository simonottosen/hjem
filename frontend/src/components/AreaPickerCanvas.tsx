// The map a search area is drawn on. It opens on the typed address and pans
// and zooms like any map; "Tegn" turns it into a canvas for one freehand
// lasso, which on release becomes the search area — or an explanation of why
// it cannot be one.
//
// The geometry lives in lib/area.ts, where it is tested. What is here is the
// part that needs a browser with WebGL: turning a stroke into screen points
// and screen points into coordinates.

import { useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import type { FeatureCollection } from "geojson";
import type { GeoJSONSource, Map as MapLibreMap } from "maplibre-gl";
import * as maplibregl from "maplibre-gl";
// The progress map's module also loads maplibre's stylesheet and points it at
// its worker, which this map needs just the same.
import { STYLE_URL } from "./ProgressMapCanvas";
import { Button } from "@/components/ui/button";
import { locateAddress, type LocatedAddress } from "@/lib/api";
import { bbox, checkArea, MAX_VERTICES, roundRing, simplify, trimLasso, type LonLat } from "@/lib/area";

const ORANGE = "#f97316";
const AREA_FILL = "hjem-area";
const AREA_LINE = "hjem-area-line";
// Closer samples add nothing simplification would not take out again, and a
// slow finger would otherwise pile up hundreds of points in one spot.
const MIN_STEP_PX = 4;

type Point = [number, number];

export interface AreaPickerProps {
  query: string;
  area: LonLat[] | null;
  onAreaChange: (area: LonLat[] | null) => void;
}

type Located =
  | { state: "loading" }
  | { state: "error"; message: string }
  | { state: "ok"; at: LocatedAddress };

function areaCollection(ring: LonLat[] | null): FeatureCollection {
  return {
    type: "FeatureCollection",
    features: ring
      ? [
          {
            type: "Feature",
            properties: {},
            geometry: { type: "Polygon", coordinates: [[...ring, ring[0]]] },
          },
        ]
      : [],
  };
}

export function AreaPickerCanvas({ query, area, onAreaChange }: AreaPickerProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MapLibreMap | null>(null);
  const readyRef = useRef(false);

  const [located, setLocated] = useState<Located>({ state: "loading" });
  // As in the progress map: the box has to exist before we know whether
  // maplibre can draw into it.
  const [broken, setBroken] = useState(false);
  const [drawing, setDrawing] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);

  // The stroke in progress, in pixels relative to the map. The ref is what the
  // handlers read, since several pointer events can arrive between renders;
  // the state only draws the line.
  const strokeRef = useRef<Point[]>([]);
  const [stroke, setStroke] = useState<Point[]>([]);
  // The one pointer drawing. A second finger landing mid-stroke is ignored
  // rather than starting a stroke of its own.
  const pointerRef = useRef<number | null>(null);

  // Read when the map finishes loading, which is later than the render that
  // created it; changes after that go through the effect further down.
  const areaRef = useRef(area);
  useEffect(() => {
    areaRef.current = area;
  }, [area]);

  useEffect(() => {
    let live = true;
    setLocated({ state: "loading" });
    locateAddress(query).then(
      (at) => live && setLocated({ state: "ok", at }),
      (err) => live && setLocated({ state: "error", message: err instanceof Error ? err.message : String(err) })
    );
    return () => {
      live = false;
    };
  }, [query]);

  const at = located.state === "ok" ? located.at : null;

  useEffect(() => {
    if (!at || !containerRef.current) return;

    const initial = areaRef.current;
    let m: MapLibreMap;
    try {
      m = new maplibregl.Map({
        container: containerRef.current,
        style: STYLE_URL,
        // An area already drawn is what the user came back to adjust, so it
        // frames the view; otherwise the address does, at street level.
        ...(initial
          ? { bounds: bbox(initial), fitBoundsOptions: { padding: 32 } }
          : { center: [at.lon, at.lat] as LonLat, zoom: 15 }),
        attributionControl: { compact: true },
      });
    } catch (err) {
      // No WebGL, most likely. The radius search does not need a map.
      console.warn("[hjem] Area map unavailable:", err);
      setBroken(true);
      return;
    }

    mapRef.current = m;
    m.on("error", (e) => console.warn("[hjem] Area map:", e.error));
    new maplibregl.Marker({ color: ORANGE }).setLngLat([at.lon, at.lat]).addTo(m);
    m.on("load", () => {
      m.addSource(AREA_FILL, { type: "geojson", data: areaCollection(areaRef.current) });
      m.addLayer({
        id: AREA_FILL,
        type: "fill",
        source: AREA_FILL,
        paint: { "fill-color": ORANGE, "fill-opacity": 0.15 },
      });
      m.addLayer({
        id: AREA_LINE,
        type: "line",
        source: AREA_FILL,
        paint: { "line-color": ORANGE, "line-width": 2 },
      });
      readyRef.current = true;
    });

    return () => {
      readyRef.current = false;
      mapRef.current = null;
      m.remove();
    };
  }, [at]);

  useEffect(() => {
    const m = mapRef.current;
    if (!m || !readyRef.current) return;
    (m.getSource(AREA_FILL) as GeoJSONSource | undefined)?.setData(areaCollection(area));
  }, [area]);

  function local(e: ReactPointerEvent<HTMLDivElement>): Point {
    const r = e.currentTarget.getBoundingClientRect();
    return [e.clientX - r.left, e.clientY - r.top];
  }

  function onPointerDown(e: ReactPointerEvent<HTMLDivElement>) {
    if (pointerRef.current !== null) return;
    pointerRef.current = e.pointerId;
    // Keeps the stroke coming when the finger strays off the map, so leaving
    // it ends the lasso where it was let go rather than where it left.
    e.currentTarget.setPointerCapture(e.pointerId);
    strokeRef.current = [local(e)];
    setStroke(strokeRef.current);
  }

  function onPointerMove(e: ReactPointerEvent<HTMLDivElement>) {
    if (e.pointerId !== pointerRef.current) return;
    const p = local(e);
    const last = strokeRef.current[strokeRef.current.length - 1];
    if (Math.hypot(p[0] - last[0], p[1] - last[1]) < MIN_STEP_PX) return;
    strokeRef.current = [...strokeRef.current, p];
    setStroke(strokeRef.current);
  }

  function onPointerUp(e: ReactPointerEvent<HTMLDivElement>) {
    if (e.pointerId !== pointerRef.current) return;
    pointerRef.current = null;
    const points = strokeRef.current;
    strokeRef.current = [];
    setStroke([]);

    const m = mapRef.current;
    // A tap, or a finger that barely moved: not an attempt at an area, so
    // drawing simply carries on.
    if (!m || points.length < 3) return;
    setDrawing(false);

    const loop = trimLasso(points);
    // Coarsened only as far as it takes to fit the server's vertex cap, so a
    // careful outline keeps its detail.
    let tolerance = 1;
    let simple = simplify(loop, tolerance);
    while (simple.length > MAX_VERTICES) {
      tolerance *= 2;
      simple = simplify(loop, tolerance);
    }
    const ring = roundRing(
      simple.map(([x, y]): LonLat => {
        const { lng, lat } = m.unproject([x, y]);
        return [lng, lat];
      })
    );
    // A failed attempt leaves the last good area standing: a slip of the
    // finger should not cost the user an outline they were happy with.
    const why = checkArea(ring);
    setProblem(why);
    if (!why) onAreaChange(ring);
  }

  // The browser took the gesture over (a system swipe, a call coming in). What
  // was drawn so far is not what the user meant to let go of.
  function onPointerCancel(e: ReactPointerEvent<HTMLDivElement>) {
    if (e.pointerId !== pointerRef.current) return;
    pointerRef.current = null;
    strokeRef.current = [];
    setStroke([]);
  }

  function startDrawing() {
    setProblem(null);
    setDrawing(true);
  }

  function stopDrawing() {
    pointerRef.current = null;
    strokeRef.current = [];
    setStroke([]);
    setDrawing(false);
  }

  if (located.state === "loading") {
    return <p className="text-sm text-muted-foreground">Finder adressen...</p>;
  }
  if (located.state === "error") {
    return <p className="text-sm text-destructive">{located.message}</p>;
  }
  if (broken) {
    return (
      <p className="text-sm text-destructive">
        Kortet kan ikke vises i denne browser, da den mangler WebGL. Søg med radius i stedet.
      </p>
    );
  }

  return (
    <div className="space-y-1">
      <div className="relative h-[320px] overflow-hidden rounded-md border sm:h-[420px]">
        <div ref={containerRef} className="size-full" />
        {drawing && (
          // Laid over the map rather than listening on its canvas, so while it
          // is up no drag, pinch or double-tap reaches maplibre's own handlers,
          // and none of them need switching off and back on. touch-action
          // keeps a one-finger stroke from scrolling the page instead.
          <div
            className="absolute inset-0 cursor-crosshair"
            style={{ touchAction: "none" }}
            onPointerDown={onPointerDown}
            onPointerMove={onPointerMove}
            onPointerUp={onPointerUp}
            onPointerCancel={onPointerCancel}
          >
            <svg className="pointer-events-none size-full">
              <polyline
                points={stroke.map((p) => p.join(",")).join(" ")}
                fill="none"
                stroke={ORANGE}
                strokeWidth={2.5}
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            </svg>
          </div>
        )}
        <div className="pointer-events-none absolute left-2 top-2 flex items-center gap-2">
          {drawing ? (
            <>
              <Button type="button" size="sm" variant="outline" className="pointer-events-auto" onClick={stopDrawing}>
                Annuller
              </Button>
              <span className="rounded-md bg-background/90 px-2 py-1 text-xs shadow-xs">
                Tegn rundt om området
              </span>
            </>
          ) : (
            <Button type="button" size="sm" className="pointer-events-auto" onClick={startDrawing}>
              {area || problem ? "Tegn igen" : "Tegn"}
            </Button>
          )}
        </div>
      </div>
      <p className="truncate text-xs">
        {problem ? (
          <span className="text-destructive">{problem}</span>
        ) : (
          <span className="text-muted-foreground">{located.at.address}</span>
        )}
      </p>
    </div>
  );
}
