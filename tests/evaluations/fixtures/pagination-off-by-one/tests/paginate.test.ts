import { describe, it, expect } from "vitest";
import { paginate } from "../src/paginate.js";

describe("paginate", () => {
  it("returns the first page", () => {
    expect(paginate([1, 2, 3, 4, 5], 1, 2)).toEqual([1, 2]);
  });

  it("returns the last page", () => {
    expect(paginate([1, 2, 3, 4, 5], 3, 2)).toEqual([5]);
  });

  it("handles a full last page", () => {
    expect(paginate([1, 2, 3, 4], 2, 2)).toEqual([3, 4]);
  });
});