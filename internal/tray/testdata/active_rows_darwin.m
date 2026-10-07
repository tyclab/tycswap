#import "tray_darwin.m"
#include <math.h>
#include <stdio.h>

void tycTrayClicked(int tag) {}

static NSTextField *findLabel(NSMenuItem *item, NSString *text) {
    for (NSView *view in item.view.subviews) {
        if ([view isKindOfClass:[NSTextField class]] && [((NSTextField *)view).stringValue isEqualToString:text]) return (NSTextField *)view;
    }
    return nil;
}

static BOOL sameColor(NSColor *a, NSColor *b, NSAppearance *appearance, BOOL diagnose) {
    if (!appearance) {
        if (diagnose) fprintf(stderr, "appearance=nil actual=unresolved expected=unresolved\n");
        return NO;
    }
    __block BOOL equal = NO;
    [appearance performAsCurrentDrawingAppearance:^{
        NSColor *first = [a colorUsingColorSpace:[NSColorSpace sRGBColorSpace]];
        NSColor *second = [b colorUsingColorSpace:[NSColorSpace sRGBColorSpace]];
        equal = first && second && fabs(first.redComponent - second.redComponent) < 0.001 &&
            fabs(first.greenComponent - second.greenComponent) < 0.001 && fabs(first.blueComponent - second.blueComponent) < 0.001;
        if (!equal && diagnose) {
            NSString *match = [appearance bestMatchFromAppearancesWithNames:
                @[NSAppearanceNameAccessibilityHighContrastAqua, NSAppearanceNameAccessibilityHighContrastDarkAqua,
                  NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
            fprintf(stderr, "appearance=%s bestMatch=%s actual=%s expected=%s\n",
                (appearance.name ?: @"nil").UTF8String, (match ?: @"nil").UTF8String,
                (first.description ?: @"nil").UTF8String, (second.description ?: @"nil").UTF8String);
        }
    }];
    return equal;
}

int main(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        NSMenuItem *active = plainRow(1, @"Account", @"Usage", NO, YES, YES, NO);
        NSMenuItem *inactive = plainRow(2, @"Account", @"Usage", NO, NO, NO, NO);
        NSMenuItem *group = plainRow(3, @"Account", @"Usage", YES, YES, YES, NO);
        NSMenuItem *gauge = gaugeRow(4, @"Account", @"Usage", -1, NO, YES, YES, NO);
        NSMenuItem *toggle = toggleRow(5, @"Model limits", @"Usage", YES, NO, NO);
        NSTextField *title = findLabel(active, @"Account");
        if (!title || active.enabled || ((TSRowView *)active.view).enabled ||
            !(title.font.fontDescriptor.symbolicTraits & NSFontBoldTrait) || !findLabel(active, @"✓")) return 1;
        for (NSMenuItem *item in @[inactive, group, gauge]) {
            if (findLabel(item, @"Account").frame.origin.x != title.frame.origin.x ||
                findLabel(item, @"Usage").frame.origin.x != title.frame.origin.x) return 2;
        }
        if (findLabel(inactive, @"✓") || findLabel(toggle, @"Model limits").font.fontDescriptor.symbolicTraits & NSFontBoldTrait) return 3;
        NSArray<NSAppearanceName> *names = @[NSAppearanceNameAqua, NSAppearanceNameDarkAqua,
            NSAppearanceNameAccessibilityHighContrastAqua, NSAppearanceNameAccessibilityHighContrastDarkAqua];
        for (NSUInteger i = 0; i < names.count; i++) {
            NSAppearance *appearance = [NSAppearance appearanceNamed:names[i]];
            NSColor *expected = i == 0 ? [NSColor colorWithSRGBRed:lightR green:lightG blue:lightB alpha:1] :
                (i == 1 ? accent() : [NSColor labelColor]);
            for (NSMenuItem *item in @[active, group, gauge]) {
                if (!sameColor(findLabel(item, @"Account").textColor, expected, appearance, YES) ||
                    !sameColor(findLabel(item, @"✓").textColor, expected, appearance, YES)) {
                    fprintf(stderr, "active color failed for appearance %lu\n", (unsigned long)i);
                    return 4;
                }
            }
            if (sameColor(findLabel(toggle, @"Model limits").textColor, accent(), appearance, NO)) return 5;
        }
    }
    return 0;
}
