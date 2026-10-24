// The VitePress default theme without its Inter font, styled by the pklnx
// design system (Carbon tokens, IBM Plex). The theme CSS comes after the
// default theme, so it overrides its variables.
import DefaultTheme from "vitepress/theme-without-fonts";
import "@pklnx/design-system/theme/vitepress.css";

export default DefaultTheme;
