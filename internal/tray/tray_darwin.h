#ifndef TYCSWAP_TRAY_DARWIN_H
#define TYCSWAP_TRAY_DARWIN_H

#include <stdint.h>

// Implemented in tray_darwin.m. Every function may be called from any thread;
// UI work is dispatched onto the main queue. tray_run must run on the main
// thread and blocks until tray_quit.
// png is the status-bar image (a template when isTemplate); brandPng (may be
// NULL) is the coloured mark shown in the menu's brand row.
// The accent colour (sRGB components 0-1) for the switches, the active marker
// and the update rows; call before tray_run.
void tray_set_accent(double r, double g, double b);
void tray_run(const void *png, int pngLen, int isTemplate, const void *brandPng, int brandLen, const char *tooltip);
void tray_quit(void);
void tray_set_title(const char *title);
void tray_set_tooltip(const char *tooltip);
// Replaces the status-bar image (sized and templated as in tray_run; an image
// wider than tall keeps its aspect, for the count badge); the brand row keeps
// its mark. png is copied before the call returns.
void tray_set_icon(const void *png, int pngLen, int isTemplate);
void tray_menu_begin(void);
// kind: 0 plain (title, optional sub line), 1 toggle (a drawn switch), 2 gauge (title, sub line, bar at pct 0-100;
// pct<0 unknown), 3 header, 4 brand (icon, product name, version/state line), 5 update (title, sub line, an
// accent arrow disc on a tinted row). dismiss: a click closes the menu.
void tray_menu_add(int tag, const char *title, const char *sub, int kind, double pct, int checked, int disabled, int separator, int dismiss);
// A submenu row titled title; the rows added until tray_menu_pop go into it.
void tray_menu_push(const char *title);
void tray_menu_pop(void);
// Shows the rows added since tray_menu_begin, in the open menu too.
void tray_menu_commit(void);
// Closes the menu if it is open.
void tray_close_menu(void);

// Implemented in Go (exported): menu item with `tag` was chosen / icon ready.
extern void tycTrayClicked(int tag);
extern void tycTrayReady(void);

#endif
