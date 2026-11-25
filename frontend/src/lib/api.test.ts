import { afterEach, expect, it, vi } from "vitest";
import { locateAddress, postBoligaIngest, startLookup } from "./api";

afterEach(() => vi.unstubAllGlobals());

function stubFetch(response: unknown) {
  const fetch = vi.fn().mockResolvedValue({
    ok: true,
    status: 202,
    json: async () => response,
  });
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

it("carries the ingest token from the lookup to the upload", async () => {
  const fetch = stubFetch({ lookup_id: "abc123", ingest_token: "s3cret" });

  const session = await startLookup("Vestergade 1", 500, 0, null);
  expect(session).toEqual({ lookupId: "abc123", ingestToken: "s3cret" });

  await postBoligaIngest(session, { fetched: [], failed: [] });

  // Without the token the server answers 403 and refetches every street
  // itself, which is the whole of the relay's saving given up.
  const body = JSON.parse(vi.mocked(fetch).mock.calls[1][1].body);
  expect(body).toMatchObject({ lookup_id: "abc123", ingest_token: "s3cret" });
});

it("sends a radius search as one range and no area", async () => {
  const fetch = stubFetch({ lookup_id: "abc123", ingest_token: "s3cret" });

  await startLookup("Vestergade 1", 500, 2, "prev");

  const body = JSON.parse(vi.mocked(fetch).mock.calls[0][1].body);
  expect(body).toEqual({ q: "Vestergade 1", ranges: [500], filter_below_std: 2, previous_lookup_id: "prev" });
});

// The server takes ranges or area, never both, so a drawn search must not
// carry the radius the form still remembers.
it("sends a drawn search as its area instead of a range", async () => {
  const fetch = stubFetch({ lookup_id: "abc123", ingest_token: "s3cret" });
  const area: [number, number][] = [
    [12.548, 55.671],
    [12.555, 55.67],
    [12.553, 55.675],
  ];

  await startLookup("Vestergade 1", 500, 2, null, area);

  const body = JSON.parse(vi.mocked(fetch).mock.calls[0][1].body);
  expect(body.area).toEqual(area);
  expect(body).not.toHaveProperty("ranges");
});

it("locates an address, or explains in the server's words why not", async () => {
  const fetch = stubFetch({ address: "Flensborggade 40, 1669 København V", lat: 55.671, lon: 12.548 });
  await expect(locateAddress("Flensborggade 40")).resolves.toEqual({
    address: "Flensborggade 40, 1669 København V",
    lat: 55.671,
    lon: 12.548,
  });
  expect(fetch).toHaveBeenCalledWith("/api/locate?q=Flensborggade%2040");

  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ error: "adressen er ikke entydig, angiv den mere præcist" }),
    })
  );
  await expect(locateAddress("Vestergade")).rejects.toThrow("Adressen er ikke entydig, angiv den mere præcist");
});
