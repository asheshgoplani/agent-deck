//go:build darwin && cgo

package desktopnotify

/*
#cgo CFLAGS: -x objective-c -fblocks
#cgo LDFLAGS: -framework Foundation -framework UserNotifications
#include <stdlib.h>
#include <dispatch/dispatch.h>
#import <Foundation/Foundation.h>
#import <UserNotifications/UserNotifications.h>

@interface ADDesktopNotificationDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

@implementation ADDesktopNotificationDelegate
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
 didReceiveNotificationResponse:(UNNotificationResponse *)response
          withCompletionHandler:(void (^)(void))completionHandler {
    NSDictionary *info = response.notification.request.content.userInfo;
    NSString *path = info[@"agent_deck_binary"];
    NSArray *arguments = info[@"agent_deck_arguments"];
    if ([path isKindOfClass:[NSString class]] && [arguments isKindOfClass:[NSArray class]] && path.length > 0) {
        @try {
            NSTask *task = [[NSTask alloc] init];
            task.launchPath = path;
            task.arguments = arguments;
            [task launch];
        } @catch (NSException *exception) {
            // A click must never crash Notification Center. Agent Deck's doctor
            // surfaces action-routing readiness before events are enabled.
        }
    }
    completionHandler();
}
@end

static ADDesktopNotificationDelegate *agentDeckDesktopNotificationDelegate;

static int ad_desktop_notify(const char *title, const char *body, const char *binary, const char *argumentsJSON) {
    @autoreleasepool {
        UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
        if (agentDeckDesktopNotificationDelegate == nil) {
            agentDeckDesktopNotificationDelegate = [[ADDesktopNotificationDelegate alloc] init];
            center.delegate = agentDeckDesktopNotificationDelegate;
        }
        NSData *argsData = [[NSString stringWithUTF8String:argumentsJSON] dataUsingEncoding:NSUTF8StringEncoding];
        NSError *jsonError = nil;
        id arguments = [NSJSONSerialization JSONObjectWithData:argsData options:0 error:&jsonError];
        if (jsonError != nil || ![arguments isKindOfClass:[NSArray class]]) return 0;
        UNMutableNotificationContent *content = [[UNMutableNotificationContent alloc] init];
        content.title = [NSString stringWithUTF8String:title];
        content.body = [NSString stringWithUTF8String:body];
        content.sound = [UNNotificationSound defaultSound];
        content.userInfo = @{@"agent_deck_binary": [NSString stringWithUTF8String:binary], @"agent_deck_arguments": arguments};
        UNNotificationRequest *request = [UNNotificationRequest requestWithIdentifier:[[NSUUID UUID] UUIDString] content:content trigger:nil];
        __block BOOL granted = NO;
        __block NSError *authorizationError = nil;
        dispatch_semaphore_t authorizationDone = dispatch_semaphore_create(0);
        [center requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound) completionHandler:^(BOOL authorizationGranted, NSError *error) {
            granted = authorizationGranted;
            authorizationError = error;
            dispatch_semaphore_signal(authorizationDone);
        }];
        dispatch_semaphore_wait(authorizationDone, DISPATCH_TIME_FOREVER);
        if (!granted && authorizationError == nil) return -1;
        if (authorizationError != nil) return 0;

        __block NSError *submissionError = nil;
        dispatch_semaphore_t submissionDone = dispatch_semaphore_create(0);
        [center addNotificationRequest:request withCompletionHandler:^(NSError *error) {
            submissionError = error;
            dispatch_semaphore_signal(submissionDone);
        }];
        dispatch_semaphore_wait(submissionDone, DISPATCH_TIME_FOREVER);
        return submissionError == nil ? 1 : 0;
    }
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"strings"
	"unsafe"
)

// NativePresentationAvailable reports whether this build can submit macOS
// UserNotifications. The helper rejects unsupported variants before accepting
// socket events that would otherwise retry forever.
func NativePresentationAvailable() bool { return true }

func NativePresent(event Event) error {
	title, body := message(event)
	command := FocusCommand(event.BinaryPath, event)
	args, err := json.Marshal(command[1:])
	if err != nil {
		return err
	}
	ctitle := C.CString(title)
	cbody := C.CString(body)
	cbinary := C.CString(command[0])
	cargs := C.CString(string(args))
	defer C.free(unsafe.Pointer(ctitle))
	defer C.free(unsafe.Pointer(cbody))
	defer C.free(unsafe.Pointer(cbinary))
	defer C.free(unsafe.Pointer(cargs))
	result := C.ad_desktop_notify(ctitle, cbody, cbinary, cargs)
	if result < 0 {
		return ErrAuthorizationDenied
	}
	if result == 0 {
		return fmt.Errorf("could not submit native macOS notification")
	}
	return nil
}

func message(event Event) (string, string) {
	title := strings.TrimSpace(event.Title)
	if title == "" {
		title = "Agent Deck session"
	}
	var prefix string
	switch event.Class {
	case Complete:
		prefix = "Completed"
	case Error:
		prefix = "Error"
	default:
		prefix = "Needs attention"
	}
	body := prefix
	if event.Summary != "" {
		body += ": " + event.Summary
	}
	return title, body
}
