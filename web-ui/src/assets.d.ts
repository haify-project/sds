// CSS is imported for its side effect; the bundler resolves it, TypeScript
// does not. TypeScript 7 refuses a side-effect import it cannot resolve
// (TS2882) where 5.x let it through, so the modules are declared here.
declare module '*.css';
declare module '@fontsource/*';
