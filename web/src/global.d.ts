// Ambient declarations for non-code imports.
//
// TypeScript 6 stopped silently allowing side-effect imports of files
// without type declarations (e.g. `import "./index.css"`). Vite handles
// these at build time, but tsc needs the module shape declared.
declare module "*.css";
declare module "*.png";
declare module "*.svg";
