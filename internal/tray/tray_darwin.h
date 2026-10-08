#ifndef TYCSWAP_TRAY_DARWIN_H
#define TYCSWAP_TRAY_DARWIN_H

#include <stdint.h>

// UI work uses the main queue; tray_run must block the main thread until tray_quit.
// Set accents before tray_run; tray_set_icon copies PNG bytes; brandPng may be NULL.
// Menu kinds: 0 plain, 1 toggle, 2 gauge (pct<0 unknown), 3 header, 4 brand, 5 update.
// push/pop nest rows; commit replaces the live menu; tycTrayClicked returns the chosen tag.

void tray_set_accent(double r, double g, double b);
void tray_set_light_accent(double r, double g, double b);
void tray_run(const void *png, int pngLen, const void *brandPng, int brandLen, const char *tooltip);
void tray_quit(void);
void tray_set_title(const char *title);
void tray_set_tooltip(const char *tooltip);
void tray_set_icon(const void *png, int pngLen);
void tray_menu_begin(void);
void tray_menu_add(int tag, const char *title, const char *sub, int kind, double pct, int checked, int active, int disabled, int separator, int dismiss);
void tray_menu_push(const char *title);
void tray_menu_pop(void);
void tray_menu_commit(void);
void tray_close_menu(void);

extern void tycTrayClicked(int tag);

#endif
