import "@testing-library/jest-dom"

// Silence the jsdom "canvas is not implemented" warning that fires whenever any
// test renders a component that touches <canvas> (e.g. charts, image previews).
// The warning is a jsdom limitation, not a product bug, and it pollutes CI output
// making real failures harder to spot.  Return null — the standard no-op stub for
// getContext when the environment does not support canvas rendering.
HTMLCanvasElement.prototype.getContext = () => null
