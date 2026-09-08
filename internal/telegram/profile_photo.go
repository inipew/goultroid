package telegram

import (
	"context"
	"fmt"
	"os"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// DownloadUserProfilePhoto downloads the current user's profile photo at Telegram's
// native large profile-photo size. It intentionally uses inputPeerPhotoFileLocation
// so the photo ID comes from the userProfilePhoto object rather than relying on a
// stale photo access hash/file reference.
func (s *Service) DownloadUserProfilePhoto(ctx context.Context, user tg.InputUserClass, dstPath string) error {
	if s == nil || s.api == nil {
		return fmt.Errorf("%w: telegram api is not initialized", core.ErrInternal)
	}
	if user == nil {
		return fmt.Errorf("%w: user is nil", core.ErrInvalidArgument)
	}
	u, ok := user.(*tg.InputUser)
	if !ok || u.UserID == 0 || u.AccessHash == 0 {
		return fmt.Errorf("%w: user must be a resolved InputUser with access hash", core.ErrInvalidArgument)
	}

	full, err := s.api.UsersGetFullUser(ctx, user)
	if err != nil {
		return mapTelegramError(err)
	}
	if full == nil {
		return fmt.Errorf("%w: empty full-user response", core.ErrNotFound)
	}

	var photoID int64
	for _, item := range full.Users {
		if candidate, ok := item.(*tg.User); ok && candidate.ID == u.UserID && candidate.ProfilePhoto != nil {
			if photo, ok := candidate.ProfilePhoto.(*tg.UserProfilePhoto); ok {
				photoID = photo.PhotoID
			}
			break
		}
	}
	if photoID == 0 {
		return fmt.Errorf("%w: user %d has no profile photo", core.ErrNotFound, u.UserID)
	}

	location := &tg.InputPeerPhotoFileLocation{
		Peer:   &tg.InputPeerUser{UserID: u.UserID, AccessHash: u.AccessHash},
		PhotoID: photoID,
		Big:    true,
	}
	if err := s.DownloadFile(ctx, location, dstPath); err != nil {
		_ = os.Remove(dstPath)
		return err
	}
	return nil
}
