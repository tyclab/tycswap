// Cocoa status item for tycswap (DESIGN A35/A37). Compiled by cgo when
// CGO_ENABLED=1 on darwin; system frameworks only.
//
// Rows are NSMenuItem custom views: a brand row (the mark, product name,
// version/state), a command row, a switch row (a switch in the accent colour
// this file draws itself), a gauge row (title, secondary line, coloured usage
// bar) and a section header. Every interactive row takes a click anywhere on
// it.
//
// A click leaves the menu open unless the row says otherwise (DESIGN A37): a
// standard NSMenuItem always closes its menu, so even plain commands are
// views, and a new menu is written into the open one rather than replacing
// it, so the switch or the toggle just clicked shows its new state in place.
#import <Cocoa/Cocoa.h>
#include "tray_darwin.h"

static const CGFloat kRowWidth = 312;
static const CGFloat kPad = 14;

// The accent colour (brand.AccentColor, set by tray_set_accent before
// tray_run): the switches, the active marker, the update rows and their arrow
// disc use it. Until it is set, tycswap's default accent (#5aa2ff).
static double accentR = 0x5a / 255.0, accentG = 0xa2 / 255.0, accentB = 0xff / 255.0;

static NSColor *accent(void) {
    return [NSColor colorWithSRGBRed:accentR green:accentG blue:accentB alpha:1];
}

void tray_set_accent(double r, double g, double b) {
    accentR = r;
    accentG = g;
    accentB = b;
}

// TSBarView: a rounded usage bar coloured by band (green < 70, amber < 90, red).
@interface TSBarView : NSView
@property (nonatomic) double pct; // < 0: unknown
@end

@implementation TSBarView
- (void)drawRect:(NSRect)dirty {
    NSRect r = self.bounds;
    NSBezierPath *track = [NSBezierPath bezierPathWithRoundedRect:r xRadius:r.size.height / 2 yRadius:r.size.height / 2];
    [[[NSColor labelColor] colorWithAlphaComponent:0.10] setFill];
    [track fill];
    if (self.pct < 0) { return; }
    double p = MIN(100.0, MAX(0.0, self.pct));
    NSRect f = r;
    f.size.width = MAX(r.size.height, r.size.width * p / 100.0);
    NSColor *fill = p >= 90 ? [NSColor systemRedColor] : (p >= 70 ? [NSColor systemOrangeColor] : [NSColor systemGreenColor]);
    [fill setFill];
    [[NSBezierPath bezierPathWithRoundedRect:f xRadius:f.size.height / 2 yRadius:f.size.height / 2] fill];
}
@end

// TSSwitchView: the on/off switch, drawn rather than an NSSwitch. AppKit
// renders a real NSSwitch in its INACTIVE appearance inside an accessory
// app's menu — a pale grey pill whether it is on or off, which is exactly the
// state the user needs to read at a glance. Drawing it keeps the accent colour
// in every appearance, and matches the gauge bars and the active-account dot.
@interface TSSwitchView : NSView
@property (nonatomic) BOOL on;
@property (nonatomic) BOOL enabled;
@end

@implementation TSSwitchView
- (void)drawRect:(NSRect)dirty {
    NSRect r = self.bounds;
    CGFloat radius = r.size.height / 2;
    NSColor *track = self.on ? accent() : [[NSColor labelColor] colorWithAlphaComponent:0.20];
    if (!self.enabled) {
        track = [track colorWithAlphaComponent:self.on ? 0.35 : 0.10];
    }
    [track setFill];
    [[NSBezierPath bezierPathWithRoundedRect:r xRadius:radius yRadius:radius] fill];

    CGFloat inset = 2;
    CGFloat d = r.size.height - inset * 2;
    CGFloat x = self.on ? NSMaxX(r) - inset - d : NSMinX(r) + inset;
    NSRect knob = NSMakeRect(x, NSMinY(r) + inset, d, d);
    [NSGraphicsContext saveGraphicsState];
    NSShadow *shadow = [NSShadow new];
    shadow.shadowColor = [[NSColor blackColor] colorWithAlphaComponent:0.30];
    shadow.shadowOffset = NSMakeSize(0, -1);
    shadow.shadowBlurRadius = 2;
    [shadow set];
    [(self.enabled ? [NSColor whiteColor] : [[NSColor whiteColor] colorWithAlphaComponent:0.6]) setFill];
    [[NSBezierPath bezierPathWithOvalInRect:knob] fill];
    [NSGraphicsContext restoreGraphicsState];
}
@end

// TSDotView: a small filled circle (the "active" marker).
@interface TSDotView : NSView
@property (nonatomic, strong) NSColor *color;
@end

@implementation TSDotView
- (void)drawRect:(NSRect)dirty {
    [self.color setFill];
    [[NSBezierPath bezierPathWithOvalInRect:self.bounds] fill];
}
@end

// TSArrowView: the update row's mark (DESIGN A44), a white arrow pointing
// up on an accent disc — the icon badge's colour.
@interface TSArrowView : NSView
@end

@implementation TSArrowView
- (void)drawRect:(NSRect)dirty {
    NSRect r = self.bounds;
    [accent() setFill];
    [[NSBezierPath bezierPathWithOvalInRect:r] fill];
    CGFloat w = r.size.width, cx = NSMidX(r);
    NSBezierPath *arrow = [NSBezierPath bezierPath];
    arrow.lineWidth = w * 0.12;
    arrow.lineCapStyle = NSLineCapStyleRound;
    arrow.lineJoinStyle = NSLineJoinStyleRound;
    // Flipped is NO: y grows upwards.
    [arrow moveToPoint:NSMakePoint(cx, NSMinY(r) + w * 0.26)];
    [arrow lineToPoint:NSMakePoint(cx, NSMaxY(r) - w * 0.25)];
    [arrow moveToPoint:NSMakePoint(cx - w * 0.2, NSMaxY(r) - w * 0.45)];
    [arrow lineToPoint:NSMakePoint(cx, NSMaxY(r) - w * 0.25)];
    [arrow lineToPoint:NSMakePoint(cx + w * 0.2, NSMaxY(r) - w * 0.45)];
    [[NSColor whiteColor] setStroke];
    [arrow stroke];
}
@end

// TSRowView: a clickable, hover-highlighted row hosting a command, a
// switch or a gauge. dismiss: the click closes the menu. tint: the row is an
// update and carries an accent tint even without the pointer on it (A44).
//
// A click shows at once, since the menu stays open (A44): the row turns the
// accent colour while pressed; a switch flips; any other row that stays open
// shows a spinner until the menu brings the outcome (its view is replaced)
// or busyTimeout passes, and takes no second click meanwhile; a row that
// closes the menu blinks first, as a native menu's row does.
@interface TSRowView : NSView
@property (nonatomic) int tag_;
@property (nonatomic) BOOL enabled;
@property (nonatomic) BOOL dismiss;
@property (nonatomic) BOOL tint;
@property (nonatomic) BOOL hover;
@property (nonatomic) BOOL pressed;
@property (nonatomic) BOOL busy;
@property (nonatomic, weak) TSSwitchView *toggle; // a switch row's switch
@property (nonatomic, weak) NSView *busyHides;     // what the spinner takes the place of
@property (nonatomic) NSRect spinnerFrame;
@property (nonatomic, strong) NSProgressIndicator *spinner;
@property (nonatomic, strong) NSTrackingArea *tracking;
@end

static const NSTimeInterval kBlink = 0.12;
static const NSTimeInterval kBusyTimeout = 4;

// after runs block on the main run loop after delay, also while the menu
// tracks the pointer (a plain timer waits for the default mode until the
// menu closes).
static void after(NSTimeInterval delay, void (^block)(void)) {
    NSTimer *t = [NSTimer timerWithTimeInterval:delay repeats:NO block:^(NSTimer *timer) { block(); }];
    [[NSRunLoop mainRunLoop] addTimer:t forMode:NSRunLoopCommonModes];
}

@implementation TSRowView
- (void)updateTrackingAreas {
    [super updateTrackingAreas];
    if (self.tracking) { [self removeTrackingArea:self.tracking]; }
    self.tracking = [[NSTrackingArea alloc] initWithRect:self.bounds
        options:(NSTrackingMouseEnteredAndExited | NSTrackingActiveInActiveApp) owner:self userInfo:nil];
    [self addTrackingArea:self.tracking];
}
// A row that replaces the one under the pointer in an open menu gets no
// mouseEntered: the pointer did not enter, it was there already.
- (void)viewDidMoveToWindow {
    [super viewDidMoveToWindow];
    if (!self.window || !self.enabled) { return; }
    NSPoint p = [self convertPoint:[self.window mouseLocationOutsideOfEventStream] fromView:nil];
    self.hover = NSPointInRect(p, self.bounds);
}
- (void)mouseEntered:(NSEvent *)e { if (self.enabled) { self.hover = YES; [self setNeedsDisplay:YES]; } }
- (void)mouseExited:(NSEvent *)e { self.hover = NO; self.pressed = NO; [self setNeedsDisplay:YES]; }
- (void)mouseDown:(NSEvent *)e {
    if (!self.enabled || self.busy) { return; }
    self.pressed = YES;
    [self setNeedsDisplay:YES];
}
// A release counts without a press on the row too: a press on the menu-bar
// icon dragged onto the row chooses it, as in a native menu.
- (void)mouseUp:(NSEvent *)e {
    if (!self.enabled || self.busy) { return; }
    int tag = self.tag_;
    self.pressed = YES;
    [self setNeedsDisplay:YES];
    if (self.dismiss) {
        NSMenu *menu = self.enclosingMenuItem.menu;
        after(kBlink, ^{
            // cancelTracking on a submenu ends the whole menu, the parent too.
            [menu cancelTracking];
            tycTrayClicked(tag);
        });
        return;
    }
    __weak TSRowView *weakSelf = self;
    if (self.toggle) {
        // The switch moves now; the menu that follows confirms it.
        self.toggle.on = !self.toggle.on;
        [self.toggle setNeedsDisplay:YES];
        after(kBlink, ^{ weakSelf.pressed = NO; [weakSelf setNeedsDisplay:YES]; });
    } else {
        [self startBusy];
        after(kBusyTimeout, ^{ [weakSelf endBusy]; });
    }
    tycTrayClicked(tag);
}
- (void)startBusy {
    self.busy = YES;
    self.busyHides.hidden = YES;
    NSProgressIndicator *p = [[NSProgressIndicator alloc] initWithFrame:self.spinnerFrame];
    p.style = NSProgressIndicatorStyleSpinning;
    p.controlSize = NSControlSizeSmall;
    p.indeterminate = YES;
    p.displayedWhenStopped = NO;
    p.usesThreadedAnimation = YES; // animates while the menu tracks the pointer
    [self addSubview:p];
    [p startAnimation:nil];
    self.spinner = p;
}
- (void)endBusy {
    if (!self.busy) { return; }
    self.busy = NO;
    self.pressed = NO;
    [self.spinner stopAnimation:nil];
    [self.spinner removeFromSuperview];
    self.spinner = nil;
    self.busyHides.hidden = NO;
    [self setNeedsDisplay:YES];
}
- (void)drawRect:(NSRect)dirty {
    NSRect r = NSInsetRect(self.bounds, 6, 1);
    if (self.pressed || self.busy) {
        [[[NSColor controlAccentColor] colorWithAlphaComponent:self.pressed && !self.busy ? 0.35 : 0.22] setFill];
        [[NSBezierPath bezierPathWithRoundedRect:r xRadius:6 yRadius:6] fill];
        return;
    }
    if (self.tint) {
        [[accent() colorWithAlphaComponent:self.hover ? 0.22 : 0.12] setFill];
        [[NSBezierPath bezierPathWithRoundedRect:r xRadius:6 yRadius:6] fill];
        return;
    }
    if (self.hover) {
        [[[NSColor labelColor] colorWithAlphaComponent:0.08] setFill];
        [[NSBezierPath bezierPathWithRoundedRect:r xRadius:6 yRadius:6] fill];
    }
}
@end

static NSStatusItem *statusItem = nil;
static NSMenu *pendingMenu = nil;
// pendingStack holds the menus tray_menu_push opened: rows go into the last.
static NSMutableArray<NSMenu *> *pendingStack = nil;
static NSImage *brandImage = nil;

// newMenu: a menu whose rows are enabled as tray_menu_add says, not by
// AppKit's validation (a view row has no action to validate).
static NSMenu *newMenu(void) {
    NSMenu *m = [[NSMenu alloc] initWithTitle:@""];
    [m setAutoenablesItems:NO];
    return m;
}

static void onMain(dispatch_block_t block) {
    if ([NSThread isMainThread]) {
        block();
    } else {
        dispatch_async(dispatch_get_main_queue(), block);
    }
}

static NSTextField *label(NSString *text, CGFloat size, BOOL secondary, NSRect frame) {
    NSTextField *l = [NSTextField labelWithString:text ?: @""];
    l.font = [NSFont systemFontOfSize:size weight:secondary ? NSFontWeightRegular : NSFontWeightMedium];
    l.textColor = secondary ? [NSColor secondaryLabelColor] : [NSColor labelColor];
    l.lineBreakMode = NSLineBreakByTruncatingTail;
    l.frame = frame;
    return l;
}

static NSMenuItem *viewItem(NSView *v, NSString *title, BOOL enabled) {
    NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:title action:nil keyEquivalent:@""];
    item.view = v;
    item.enabled = enabled;
    return item;
}

// brandRow: the mark, product name, and the version/state lines ("\n"
// separates them in sub), the text block centred beside the mark.
static NSMenuItem *brandRow(NSString *title, NSString *sub) {
    NSArray<NSString *> *lines = sub.length ? [sub componentsSeparatedByString:@"\n"] : @[];
    const CGFloat lineH = 15;
    CGFloat n = lines.count;
    CGFloat h = 56 + (n > 1 ? lineH * (n - 1) : 0);
    NSView *v = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, kRowWidth, h)];
    CGFloat iconSize = 32;
    CGFloat x = kPad;
    if (brandImage) {
        NSImageView *iv = [NSImageView imageViewWithImage:brandImage];
        iv.imageScaling = NSImageScaleProportionallyUpOrDown;
        iv.frame = NSMakeRect(kPad, (h - iconSize) / 2, iconSize, iconSize);
        [v addSubview:iv];
        x = kPad + iconSize + 10;
    }
    CGFloat textW = kRowWidth - x - kPad;
    CGFloat bottom = (h - (20 + lineH * n)) / 2;
    NSTextField *t = label(title, 14, NO, NSMakeRect(x, bottom + lineH * n + 2, textW, 18));
    t.font = [NSFont systemFontOfSize:14 weight:NSFontWeightSemibold];
    [v addSubview:t];
    for (NSUInteger i = 0; i < lines.count; i++) {
        [v addSubview:label(lines[i], 11.5, YES, NSMakeRect(x, bottom + lineH * (n - 1 - i), textW, lineH))];
    }
    return viewItem(v, title, NO);
}

// plainRow: a command, with an optional secondary line under its title.
static NSMenuItem *plainRow(int tag, NSString *title, NSString *sub, BOOL disabled, BOOL dismiss) {
    CGFloat h = sub.length ? 40 : 24;
    TSRowView *v = [[TSRowView alloc] initWithFrame:NSMakeRect(0, 0, kRowWidth, h)];
    v.tag_ = tag;
    v.enabled = !disabled;
    v.dismiss = dismiss;
    v.spinnerFrame = NSMakeRect(kRowWidth - kPad - 16, (h - 16) / 2, 16, 16);
    CGFloat textW = kRowWidth - 2 * kPad - 20; // room for the spinner
    NSTextField *t;
    if (sub.length) {
        t = label(title, 13, NO, NSMakeRect(kPad, 19, textW, 18));
        NSTextField *s = label(sub, 11, YES, NSMakeRect(kPad, 4, textW, 15));
        if (disabled) { s.textColor = [NSColor tertiaryLabelColor]; }
        [v addSubview:s];
    } else {
        t = label(title, 13, NO, NSMakeRect(kPad, (h - 18) / 2, textW, 18));
    }
    t.font = [NSFont systemFontOfSize:13 weight:NSFontWeightRegular];
    if (disabled) { t.textColor = [NSColor tertiaryLabelColor]; }
    [v addSubview:t];
    return viewItem(v, title, !disabled);
}

// updateRow: an update to install (A44) — the arrow disc, the title in
// semibold, what it brings on the line below, on an accent-tinted row.
static NSMenuItem *updateRow(int tag, NSString *title, NSString *sub, BOOL disabled, BOOL dismiss) {
    CGFloat h = sub.length ? 44 : 30;
    TSRowView *v = [[TSRowView alloc] initWithFrame:NSMakeRect(0, 0, kRowWidth, h)];
    v.tag_ = tag;
    v.enabled = !disabled;
    v.dismiss = dismiss;
    v.tint = !disabled;
    v.spinnerFrame = NSMakeRect(kRowWidth - kPad - 16, (h - 16) / 2, 16, 16);
    const CGFloat d = 22;
    TSArrowView *arrow = [[TSArrowView alloc] initWithFrame:NSMakeRect(kPad, (h - d) / 2, d, d)];
    [v addSubview:arrow];
    CGFloat x = kPad + d + 10;
    CGFloat textW = kRowWidth - x - kPad - 20; // room for the spinner
    // The trailing "…" (a dialog follows) belongs to text menus; the row is
    // already marked as one that acts.
    NSString *t = [title hasSuffix:@"…"] ? [title substringToIndex:title.length - 1] : title;
    NSTextField *tl;
    if (sub.length) {
        tl = label(t, 13, NO, NSMakeRect(x, 22, textW, 18));
        [v addSubview:label(sub, 11, YES, NSMakeRect(x, 6, textW, 15))];
    } else {
        tl = label(t, 13, NO, NSMakeRect(x, (h - 18) / 2, textW, 18));
    }
    tl.font = [NSFont systemFontOfSize:13 weight:NSFontWeightSemibold];
    [v addSubview:tl];
    return viewItem(v, title, !disabled);
}

static NSMenuItem *toggleRow(int tag, NSString *title, NSString *sub, BOOL on, BOOL disabled, BOOL dismiss) {
    CGFloat h = sub.length ? 46 : 32;
    // The whole row is the click target, as on the gauge rows: a 34 pt switch
    // is a small thing to aim at in a menu.
    TSRowView *v = [[TSRowView alloc] initWithFrame:NSMakeRect(0, 0, kRowWidth, h)];
    v.tag_ = tag;
    v.enabled = !disabled;
    v.dismiss = dismiss;

    const CGFloat swW = 34, swH = 20;
    TSSwitchView *sw = [[TSSwitchView alloc] initWithFrame:
        NSMakeRect(kRowWidth - kPad - swW, (h - swH) / 2, swW, swH)];
    sw.on = on;
    sw.enabled = !disabled;
    v.toggle = sw;

    CGFloat textW = NSMinX(sw.frame) - kPad - 10;
    if (sub.length) {
        [v addSubview:label(title, 13, NO, NSMakeRect(kPad, 24, textW, 18))];
        [v addSubview:label(sub, 11, YES, NSMakeRect(kPad, 7, textW, 15))];
    } else {
        [v addSubview:label(title, 13, NO, NSMakeRect(kPad, (h - 18) / 2, textW, 18))];
    }
    [v addSubview:sw];
    return viewItem(v, title, !disabled);
}

static NSMenuItem *gaugeRow(int tag, NSString *title, NSString *sub, double pct, BOOL on, BOOL disabled, BOOL dismiss) {
    CGFloat h = 50;
    TSRowView *v = [[TSRowView alloc] initWithFrame:NSMakeRect(0, 0, kRowWidth, h)];
    v.tag_ = tag;
    v.enabled = !disabled;
    v.dismiss = dismiss;
    // active marker column: an accent dot for the account Claude Code is on
    if (on) {
        TSDotView *dot = [[TSDotView alloc] initWithFrame:NSMakeRect(kPad, 33, 7, 7)];
        dot.color = accent();
        [v addSubview:dot];
    }
    CGFloat x = kPad + 15;
    CGFloat textW = kRowWidth - x - kPad - 46;
    NSTextField *t = label(title, 13, NO, NSMakeRect(x, 27, textW, 18));
    if (on) { t.font = [NSFont systemFontOfSize:13 weight:NSFontWeightSemibold]; }
    if (disabled && !on) { t.textColor = [NSColor tertiaryLabelColor]; }
    [v addSubview:t];
    NSString *pctText = pct < 0 ? @"—" : [NSString stringWithFormat:@"%.0f%%", pct];
    NSTextField *p = label(pctText, 12, YES, NSMakeRect(kRowWidth - kPad - 42, 27, 42, 18));
    p.alignment = NSTextAlignmentRight;
    p.font = [NSFont monospacedDigitSystemFontOfSize:12 weight:NSFontWeightMedium];
    if (pct >= 90) { p.textColor = [NSColor systemRedColor]; }
    else if (pct >= 70) { p.textColor = [NSColor systemOrangeColor]; }
    [v addSubview:p];
    v.busyHides = p;
    v.spinnerFrame = NSMakeRect(kRowWidth - kPad - 16, 28, 16, 16);
    TSBarView *bar = [[TSBarView alloc] initWithFrame:NSMakeRect(x, 20, kRowWidth - x - kPad, 4)];
    bar.pct = pct;
    [v addSubview:bar];
    [v addSubview:label(sub, 11, YES, NSMakeRect(x, 3, kRowWidth - x - kPad, 14))];
    return viewItem(v, title, !disabled);
}

static NSMenuItem *headerRow(NSString *title) {
    NSView *v = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, kRowWidth, 22)];
    NSTextField *l = label([title uppercaseString], 10.5, YES, NSMakeRect(kPad, 4, kRowWidth - 2 * kPad, 14));
    l.font = [NSFont systemFontOfSize:10.5 weight:NSFontWeightSemibold];
    l.textColor = [NSColor tertiaryLabelColor];
    [v addSubview:l];
    return viewItem(v, title, NO);
}

// barImage: the status-bar rendition of a PNG, 18 pt high whatever its pixel
// size (a 128 px mark stays crisp on Retina) and as wide as its aspect says
// (the count badge reaches past the square, A44), a template when asked.
static NSImage *barImage(NSData *data, int isTemplate) {
    NSImage *image = [[NSImage alloc] initWithData:data];
    NSImageRep *rep = image.representations.firstObject;
    CGFloat aspect = rep && rep.pixelsHigh > 0 ? (CGFloat)rep.pixelsWide / (CGFloat)rep.pixelsHigh : 1;
    [image setSize:NSMakeSize(18 * aspect, 18)];
    [image setTemplate:isTemplate ? YES : NO];
    return image;
}

void tray_run(const void *png, int pngLen, int isTemplate, const void *brandPng, int brandLen, const char *tooltip) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        // Accessory: no Dock icon, no menu bar takeover — a background helper
        // that lives in the status bar.
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
        statusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
        statusItem.button.image = barImage([NSData dataWithBytes:png length:pngLen], isTemplate);
        statusItem.button.imagePosition = NSImageLeft;
        if (brandPng && brandLen > 0) {
            brandImage = [[NSImage alloc] initWithData:[NSData dataWithBytes:brandPng length:brandLen]];
        }
        if (tooltip) {
            statusItem.button.toolTip = [NSString stringWithUTF8String:tooltip];
        }
        statusItem.menu = newMenu();
        dispatch_async(dispatch_get_main_queue(), ^{ tycTrayReady(); });
        [NSApp run];
    }
}

void tray_quit(void) {
    onMain(^{
        if (statusItem) {
            [[NSStatusBar systemStatusBar] removeStatusItem:statusItem];
            statusItem = nil;
        }
        [NSApp stop:nil];
        // stop: takes effect after the next event; post one so run returns now.
        NSEvent *wake = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
                                           location:NSZeroPoint modifierFlags:0 timestamp:0
                                       windowNumber:0 context:nil subtype:0 data1:0 data2:0];
        [NSApp postEvent:wake atStart:YES];
    });
}

void tray_set_title(const char *title) {
    NSString *s = title ? [NSString stringWithUTF8String:title] : @"";
    onMain(^{
        if (statusItem) {
            statusItem.button.title = s;
            statusItem.button.imagePosition = [s length] ? NSImageLeft : NSImageOnly;
        }
    });
}

void tray_set_tooltip(const char *tooltip) {
    NSString *s = tooltip ? [NSString stringWithUTF8String:tooltip] : @"";
    onMain(^{ if (statusItem) { statusItem.button.toolTip = s; } });
}

void tray_set_icon(const void *png, int pngLen, int isTemplate) {
    // Copy now: png is Go memory, only valid for the duration of this call.
    NSData *data = [NSData dataWithBytes:png length:pngLen];
    onMain(^{
        if (!statusItem) { return; }
        // A PNG AppKit cannot decode keeps the icon that is there.
        NSImage *image = barImage(data, isTemplate);
        if (image) { statusItem.button.image = image; }
    });
}

// sameShape: whether row o can take n's place: both separators, or both rows
// with a submenu, or both without.
static BOOL sameShape(NSMenuItem *o, NSMenuItem *n) {
    if (o.isSeparatorItem || n.isSeparatorItem) { return o.isSeparatorItem && n.isSeparatorItem; }
    return (o.submenu != nil) == (n.submenu != nil);
}

// syncMenu makes dst show src's rows and empties src. A row of the same shape
// is kept and given the new view, so an open menu (and an open submenu, which
// closes when its row goes) follows the change in place; only the rest is
// inserted or removed.
static void syncMenu(NSMenu *dst, NSMenu *src) {
    NSArray<NSMenuItem *> *rows = [src.itemArray copy];
    [src removeAllItems];
    NSInteger i = 0;
    for (NSMenuItem *n in rows) {
        NSMenuItem *o = i < dst.numberOfItems ? [dst itemAtIndex:i] : nil;
        if (o && sameShape(o, n)) {
            if (!n.isSeparatorItem) {
                NSView *v = n.view;
                n.view = nil; // a view belongs to one row at a time
                o.view = v;
                o.title = n.title;
                o.enabled = n.enabled;
                if (n.submenu) {
                    NSMenu *sub = n.submenu;
                    n.submenu = nil;
                    syncMenu(o.submenu, sub);
                }
            }
        } else {
            if (o) { [dst removeItemAtIndex:i]; }
            [dst insertItem:n atIndex:i];
        }
        i++;
    }
    while (dst.numberOfItems > i) { [dst removeItemAtIndex:i]; }
}

void tray_menu_begin(void) {
    onMain(^{
        pendingMenu = newMenu();
        pendingStack = [NSMutableArray arrayWithObject:pendingMenu];
    });
}

void tray_menu_add(int tag, const char *title, const char *sub, int kind, double pct, int checked, int disabled, int separator, int dismiss) {
    NSString *s = title ? [NSString stringWithUTF8String:title] : @"";
    NSString *subS = sub ? [NSString stringWithUTF8String:sub] : @"";
    onMain(^{
        NSMenu *menu = pendingStack.lastObject;
        if (!menu) { return; }
        if (separator) {
            [menu addItem:[NSMenuItem separatorItem]];
            return;
        }
        switch (kind) {
        case 1:
            [menu addItem:toggleRow(tag, s, subS, checked != 0, disabled != 0, dismiss != 0)];
            return;
        case 2:
            [menu addItem:gaugeRow(tag, s, subS, pct, checked != 0, disabled != 0, dismiss != 0)];
            return;
        case 3:
            [menu addItem:headerRow(s)];
            return;
        case 4:
            [menu addItem:brandRow(s, subS)];
            return;
        case 5:
            [menu addItem:updateRow(tag, s, subS, disabled != 0, dismiss != 0)];
            return;
        }
        [menu addItem:plainRow(tag, s, subS, disabled != 0, dismiss != 0)];
    });
}

void tray_menu_push(const char *title) {
    NSString *s = title ? [NSString stringWithUTF8String:title] : @"";
    onMain(^{
        NSMenu *menu = pendingStack.lastObject;
        if (!menu) { return; }
        // A standard row: AppKit opens its submenu on hover and draws the arrow.
        NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:s action:nil keyEquivalent:@""];
        item.enabled = YES;
        NSMenu *sub = newMenu();
        item.submenu = sub;
        [menu addItem:item];
        [pendingStack addObject:sub];
    });
}

void tray_menu_pop(void) {
    onMain(^{
        if (pendingStack.count > 1) { [pendingStack removeLastObject]; }
    });
}

void tray_menu_commit(void) {
    onMain(^{
        if (statusItem && pendingMenu) {
            if (statusItem.menu) {
                syncMenu(statusItem.menu, pendingMenu);
            } else {
                statusItem.menu = pendingMenu;
            }
        }
        pendingMenu = nil;
        pendingStack = nil;
    });
}

void tray_close_menu(void) {
    onMain(^{ if (statusItem) { [statusItem.menu cancelTracking]; } });
}
